package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/techdox/trove/internal/agentkit"
	"github.com/techdox/trove/pkg/model"
)

// collector maps a Kubernetes cluster into a single Trove host snapshot:
// Deployments/StatefulSets/DaemonSets become parent services and their Pods
// become child instances linked via parent_external_id.
type collector struct {
	cli *kubeClient
	cfg kubeConfig
	log *slog.Logger
}

func (c *collector) Collect(ctx context.Context) ([]agentkit.HostSnapshot, error) {
	var deployments, statefulsets, daemonsets workloadList
	if err := c.cli.get(ctx, c.apiPath("/apis/apps/v1", "deployments"), &deployments); err != nil {
		return nil, err
	}
	if err := c.cli.get(ctx, c.apiPath("/apis/apps/v1", "statefulsets"), &statefulsets); err != nil {
		return nil, err
	}
	if err := c.cli.get(ctx, c.apiPath("/apis/apps/v1", "daemonsets"), &daemonsets); err != nil {
		return nil, err
	}
	var replicasets replicaSetList
	if err := c.cli.get(ctx, c.apiPath("/apis/apps/v1", "replicasets"), &replicasets); err != nil {
		return nil, err
	}
	var pods podList
	if err := c.cli.get(ctx, c.apiPath("/api/v1", "pods"), &pods); err != nil {
		return nil, err
	}

	services := make([]model.ReportService, 0, len(pods.Items)+len(deployments.Items))

	for i := range deployments.Items {
		w := &deployments.Items[i]
		services = append(services, workloadService(model.KindDeployment, w, replicaCount(w), w.Status.ReadyReplicas))
	}
	for i := range statefulsets.Items {
		w := &statefulsets.Items[i]
		services = append(services, workloadService(model.KindStatefulSet, w, replicaCount(w), w.Status.ReadyReplicas))
	}
	for i := range daemonsets.Items {
		w := &daemonsets.Items[i]
		services = append(services, workloadService(model.KindDaemonSet, w, w.Status.DesiredNumberScheduled, w.Status.NumberReady))
	}

	// Map ReplicaSet -> owning Deployment so pods can resolve to their Deployment.
	rsOwner := map[string]string{}
	for _, rs := range replicasets.Items {
		for _, o := range rs.Metadata.OwnerReferences {
			if o.Kind == "Deployment" {
				rsOwner[rs.Metadata.Namespace+"/"+rs.Metadata.Name] = extID("deployment", rs.Metadata.Namespace, o.Name)
			}
		}
	}

	for i := range pods.Items {
		services = append(services, podService(&pods.Items[i], rsOwner))
	}

	// Fetch cluster version from the discovery API.
	meta := map[string]string{"platform": string(model.PlatformKubernetes)}
	if sv, err := c.cli.serverVersion(ctx); err != nil {
		c.log.Debug("kube server version failed", "err", err)
	} else {
		if sv.GitVersion != "" {
			meta["kubernetes.version"] = sv.GitVersion
		}
		if sv.Platform != "" {
			meta["kubernetes.platform"] = sv.Platform
		}
	}

	condition, metrics, nodeMeta := c.clusterHealth(ctx)
	for key, value := range nodeMeta {
		meta[key] = value
	}

	return []agentkit.HostSnapshot{{
		Host: model.ReportHost{
			Hostname:  c.cfg.cluster,
			Condition: condition,
			Metrics:   metrics,
			Meta:      meta,
		},
		Services: services,
	}}, nil
}

// clusterHealth rolls node readiness into one cluster condition and, when the
// optional Metrics API is installed, aggregates its current CPU and memory
// usage. Both reads are best-effort so upgrading an existing deployment before
// applying the expanded read-only RBAC cannot stop workload reports.
func (c *collector) clusterHealth(ctx context.Context) (model.HostCondition, *model.HostMetrics, map[string]string) {
	var nodes nodeList
	if err := c.cli.get(ctx, "/api/v1/nodes", &nodes); err != nil {
		c.log.Debug("kube nodes unavailable; host condition and capacity omitted", "err", err)
		return model.HostConditionUnknown, nil, nil
	}

	condition, ready := nodeCondition(nodes.Items)
	meta := map[string]string{
		"kubernetes.nodes":       strconv.Itoa(len(nodes.Items)),
		"kubernetes.ready_nodes": strconv.Itoa(ready),
	}

	var usage nodeMetricsList
	if err := c.cli.get(ctx, "/apis/metrics.k8s.io/v1beta1/nodes", &usage); err != nil {
		c.log.Debug("kube Metrics API unavailable; reporting node capacity without usage", "err", err)
		meta["kubernetes.metrics_api"] = "unavailable"
		return condition, aggregateNodeMetrics(nodes.Items, nil), meta
	}
	meta["kubernetes.metrics_api"] = "available"
	return condition, aggregateNodeMetrics(nodes.Items, &usage), meta
}

func nodeCondition(nodes []kubeNode) (model.HostCondition, int) {
	if len(nodes) == 0 {
		return model.HostConditionUnknown, 0
	}
	ready, notReady := 0, 0
	for i := range nodes {
		status := ""
		for _, condition := range nodes[i].Status.Conditions {
			if condition.Type == "Ready" {
				status = condition.Status
				break
			}
		}
		switch status {
		case "True":
			ready++
		case "False":
			notReady++
		}
	}
	if ready == len(nodes) {
		return model.HostConditionNormal, ready
	}
	if ready > 0 {
		return model.HostConditionWarning, ready
	}
	if notReady > 0 {
		return model.HostConditionCritical, ready
	}
	return model.HostConditionUnknown, ready
}

func aggregateNodeMetrics(nodes []kubeNode, usage *nodeMetricsList) *model.HostMetrics {
	byName := make(map[string]kubeNode, len(nodes))
	totalCores := 0.0
	for _, node := range nodes {
		byName[node.Metadata.Name] = node
		if cores, ok := parseCPUQuantity(node.Status.Capacity["cpu"]); ok {
			totalCores += cores
		}
	}

	m := &model.HostMetrics{}
	populated := false
	if totalCores > 0 && totalCores <= float64(math.MaxInt) {
		count := int(math.Round(totalCores))
		if count > 0 {
			m.CPULogicalCount = &count
			populated = true
		}
	}
	if usage == nil {
		if populated {
			return m
		}
		return nil
	}

	usedCPU, capacityCPU := 0.0, 0.0
	var usedMemory, capacityMemory uint64
	for _, item := range usage.Items {
		node, ok := byName[item.Metadata.Name]
		if !ok {
			continue
		}
		if used, ok := parseCPUQuantity(item.Usage["cpu"]); ok {
			if capacity, ok := parseCPUQuantity(node.Status.Capacity["cpu"]); ok && capacity > 0 {
				usedCPU += used
				capacityCPU += capacity
			}
		}
		if used, ok := parseByteQuantity(item.Usage["memory"]); ok {
			if capacity, ok := parseByteQuantity(node.Status.Capacity["memory"]); ok && capacity > 0 && used <= capacity {
				if math.MaxUint64-usedMemory >= used && math.MaxUint64-capacityMemory >= capacity {
					usedMemory += used
					capacityMemory += capacity
				}
			}
		}
	}
	if capacityCPU > 0 {
		ratio := math.Min(usedCPU/capacityCPU, 1)
		if ratio >= 0 && !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
			m.CPUUsageRatio = &ratio
			populated = true
		}
	}
	if capacityMemory > 0 && usedMemory <= capacityMemory {
		m.Memory = &model.HostResourceUsage{UsedBytes: usedMemory, TotalBytes: capacityMemory}
		populated = true
	}
	if !populated {
		return nil
	}
	return m
}

func parseCPUQuantity(raw string) (float64, bool) {
	return parseScaledQuantity(raw, map[string]float64{
		"n": 1e-9,
		"u": 1e-6,
		"m": 1e-3,
		"":  1,
	})
}

func parseByteQuantity(raw string) (uint64, bool) {
	value, ok := parseScaledQuantity(raw, map[string]float64{
		"Ki": 1 << 10,
		"Mi": 1 << 20,
		"Gi": 1 << 30,
		"Ti": 1 << 40,
		"Pi": 1 << 50,
		"k":  1e3,
		"K":  1e3,
		"M":  1e6,
		"G":  1e9,
		"T":  1e12,
		"P":  1e15,
		"":   1,
	})
	if !ok || value < 0 || value > math.MaxUint64 {
		return 0, false
	}
	return uint64(math.Round(value)), true
}

func parseScaledQuantity(raw string, scales map[string]float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	for _, suffix := range []string{"Pi", "Ti", "Gi", "Mi", "Ki", "n", "u", "m", "P", "T", "G", "M", "K", "k", ""} {
		scale, supported := scales[suffix]
		if !supported || !strings.HasSuffix(raw, suffix) {
			continue
		}
		number := strings.TrimSuffix(raw, suffix)
		if number == "" {
			return 0, false
		}
		value, err := strconv.ParseFloat(number, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false
		}
		return value * scale, true
	}
	return 0, false
}

func replicaCount(w *workload) int {
	if w.Spec.Replicas != nil {
		return *w.Spec.Replicas
	}
	return 1 // Kubernetes default when unset
}

func workloadService(kind model.Kind, w *workload, desired, ready int) model.ReportService {
	image := ""
	if cs := w.Spec.Template.Spec.Containers; len(cs) > 0 {
		image = cs[0].Image
	}
	return model.ReportService{
		ExternalID: extID(string(kind), w.Metadata.Namespace, w.Metadata.Name),
		Name:       w.Metadata.Name,
		Kind:       kind,
		Image:      image,
		State:      fmt.Sprintf("%d/%d", ready, desired),
		Health:     replicaHealth(desired, ready),
		Labels:     map[string]string{"namespace": w.Metadata.Namespace},
	}
}

func podService(p *pod, rsOwner map[string]string) model.ReportService {
	image, digest := "", ""
	if len(p.Status.ContainerStatuses) > 0 {
		image = p.Status.ContainerStatuses[0].Image
		digest = digestFromImageID(p.Status.ContainerStatuses[0].ImageID)
	} else if len(p.Spec.Containers) > 0 {
		image = p.Spec.Containers[0].Image
	}
	health := podHealth(p)
	return model.ReportService{
		ExternalID:       extID("pod", p.Metadata.Namespace, p.Metadata.Name),
		ParentExternalID: podParent(p, rsOwner),
		Name:             p.Metadata.Name,
		Kind:             model.KindPod,
		Image:            image,
		ImageDigest:      digest,
		State:            strings.ToLower(p.Status.Phase),
		Health:           health,
		HealthDetail:     podDetail(p, health),
		Labels:           map[string]string{"namespace": p.Metadata.Namespace, "node": p.Spec.NodeName},
	}
}

// podDetail explains why a pod is unhealthy: the first not-ready container's
// waiting reason (CrashLoopBackOff, ImagePullBackOff, …) or termination reason
// (OOMKilled, Error + exit code), falling back to the pod-level reason
// (Evicted, …). Empty for healthy/unknown pods.
func podDetail(p *pod, health model.Health) string {
	if health != model.HealthUnhealthy {
		return ""
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			continue
		}
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			return joinReason(cs.Name, w.Reason, w.Message)
		}
		if t := cs.State.Terminated; t != nil && (t.Reason != "" || t.ExitCode != 0) {
			reason := t.Reason
			if reason == "" {
				reason = "Terminated"
			}
			return joinReason(cs.Name, fmt.Sprintf("%s (exit %d)", reason, t.ExitCode), t.Message)
		}
	}
	if p.Status.Reason != "" {
		return joinReason("", p.Status.Reason, p.Status.Message)
	}
	return ""
}

// joinReason formats "<container>: <reason> — <message>", trimming and capping
// the free-form message.
func joinReason(container, reason, message string) string {
	s := reason
	if container != "" {
		s = container + ": " + reason
	}
	if msg := strings.Join(strings.Fields(message), " "); msg != "" {
		if len(msg) > 240 {
			msg = msg[:240] + "…"
		}
		s += " — " + msg
	}
	return s
}

func podParent(p *pod, rsOwner map[string]string) string {
	for _, o := range p.Metadata.OwnerReferences {
		switch o.Kind {
		case "ReplicaSet":
			if dep, ok := rsOwner[p.Metadata.Namespace+"/"+o.Name]; ok {
				return dep
			}
		case "StatefulSet":
			return extID("statefulset", p.Metadata.Namespace, o.Name)
		case "DaemonSet":
			return extID("daemonset", p.Metadata.Namespace, o.Name)
		}
	}
	return "" // standalone pod (e.g. Job-owned or bare)
}

func replicaHealth(desired, ready int) model.Health {
	switch {
	case desired <= 0:
		return model.HealthUnknown
	case ready >= desired:
		return model.HealthHealthy
	default:
		return model.HealthUnhealthy
	}
}

func podHealth(p *pod) model.Health {
	switch p.Status.Phase {
	case "Running":
		if podReady(p) {
			return model.HealthHealthy
		}
		return model.HealthUnhealthy
	case "Failed":
		return model.HealthUnhealthy
	default: // Pending, Succeeded, Unknown
		return model.HealthUnknown
	}
}

func podReady(p *pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// digestFromImageID extracts the manifest digest from a pod container's
// imageID, which looks like "docker.io/library/nginx@sha256:..." or
// "docker-pullable://repo@sha256:...".
func digestFromImageID(imageID string) string {
	if at := strings.LastIndex(imageID, "@"); at >= 0 {
		return imageID[at+1:]
	}
	return ""
}

func extID(kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", kind, namespace, name)
}
