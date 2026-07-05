package main

import (
	"context"
	"fmt"
	"log/slog"
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

	return []agentkit.HostSnapshot{{
		Host: model.ReportHost{
			Hostname: c.cfg.cluster,
			Meta:     map[string]string{"platform": "kubernetes"},
		},
		Services: services,
	}}, nil
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
