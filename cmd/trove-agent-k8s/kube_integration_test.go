package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func TestKubernetesCollectorIntegration(t *testing.T) {
	t.Parallel()

	responses := map[string]string{
		"/apis/apps/v1/namespaces/prod/deployments":  `{"items":[{"metadata":{"name":"web","namespace":"prod"},"spec":{"replicas":2,"template":{"spec":{"containers":[{"name":"web","image":"ghcr.io/acme/web:v2"}]}}},"status":{"replicas":2,"readyReplicas":1}}]}`,
		"/apis/apps/v1/namespaces/prod/statefulsets": `{"items":[{"metadata":{"name":"db","namespace":"prod"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"db","image":"postgres:17"}]}}},"status":{"replicas":1,"readyReplicas":1}}]}`,
		"/apis/apps/v1/namespaces/prod/daemonsets":   `{"items":[{"metadata":{"name":"logs","namespace":"prod"},"spec":{"template":{"spec":{"containers":[{"name":"logs","image":"fluent/fluent-bit:3"}]}}},"status":{"desiredNumberScheduled":2,"numberReady":2}}]}`,
		"/apis/apps/v1/namespaces/prod/replicasets":  `{"items":[{"metadata":{"name":"web-7f8d","namespace":"prod","ownerReferences":[{"kind":"Deployment","name":"web"}]}}]}`,
		"/api/v1/namespaces/prod/pods": `{"items":[
			{"metadata":{"name":"web-7f8d-a","namespace":"prod","ownerReferences":[{"kind":"ReplicaSet","name":"web-7f8d"}]},"spec":{"nodeName":"node-a","containers":[{"name":"web","image":"ghcr.io/acme/web:v2"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"web","image":"ghcr.io/acme/web:v2","imageID":"containerd://ghcr.io/acme/web@sha256:abc","ready":true}]}},
			{"metadata":{"name":"db-0","namespace":"prod","ownerReferences":[{"kind":"StatefulSet","name":"db"}]},"spec":{"nodeName":"node-b","containers":[{"name":"db","image":"postgres:17"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"db","image":"postgres:17","ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff","message":"back-off restarting failed container"}}}]}},
			{"metadata":{"name":"logs-node-a","namespace":"prod","ownerReferences":[{"kind":"DaemonSet","name":"logs"}]},"spec":{"nodeName":"node-a","containers":[{"name":"logs","image":"fluent/fluent-bit:3"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"logs","image":"fluent/fluent-bit:3","ready":true}]}}
		]}`,
		"/version": `{"gitVersion":"v1.34.1","platform":"linux/amd64"}`,
		"/api/v1/nodes": `{"items":[
			{"metadata":{"name":"node-a"},"status":{"capacity":{"cpu":"4","memory":"8Gi"},"conditions":[{"type":"Ready","status":"True"}]}},
			{"metadata":{"name":"node-b"},"status":{"capacity":{"cpu":"4","memory":"8Gi"},"conditions":[{"type":"Ready","status":"False"}]}}
		]}`,
		"/apis/metrics.k8s.io/v1beta1/nodes": `{"items":[
			{"metadata":{"name":"node-a"},"usage":{"cpu":"1","memory":"2Gi"}},
			{"metadata":{"name":"node-b"},"usage":{"cpu":"2","memory":"4Gi"}}
		]}`,
	}

	var mu sync.Mutex
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only client must use GET", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer integration-token" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			http.Error(w, "missing JSON accept header", http.StatusNotAcceptable)
			return
		}
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	col := &collector{
		cli: &kubeClient{http: server.Client(), base: server.URL, token: "integration-token"},
		cfg: kubeConfig{cluster: "home-prod", namespace: "prod"},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	snapshots, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.Host.Hostname != "home-prod" || snapshot.Host.Condition != model.HostConditionWarning {
		t.Fatalf("host = %+v, want home-prod warning", snapshot.Host)
	}
	if got := snapshot.Host.Meta["kubernetes.version"]; got != "v1.34.1" {
		t.Fatalf("kubernetes.version = %q", got)
	}
	if got := snapshot.Host.Meta["kubernetes.ready_nodes"]; got != "1" {
		t.Fatalf("kubernetes.ready_nodes = %q", got)
	}
	if got := snapshot.Host.Meta["kubernetes.metrics_api"]; got != "available" {
		t.Fatalf("kubernetes.metrics_api = %q", got)
	}
	if snapshot.Host.Metrics == nil || snapshot.Host.Metrics.CPULogicalCount == nil ||
		*snapshot.Host.Metrics.CPULogicalCount != 8 || snapshot.Host.Metrics.CPUUsageRatio == nil ||
		*snapshot.Host.Metrics.CPUUsageRatio != 0.375 || snapshot.Host.Metrics.Memory == nil ||
		snapshot.Host.Metrics.Memory.UsedBytes != 6*(1<<30) {
		t.Fatalf("host metrics = %+v", snapshot.Host.Metrics)
	}

	if len(snapshot.Services) != 6 {
		t.Fatalf("services = %d, want 6", len(snapshot.Services))
	}
	byID := make(map[string]model.ReportService, len(snapshot.Services))
	for _, service := range snapshot.Services {
		byID[service.ExternalID] = service
	}
	if got := byID["deployment/prod/web"]; got.Kind != model.KindDeployment || got.State != "1/2" || got.Health != model.HealthUnhealthy {
		t.Fatalf("deployment = %+v", got)
	}
	if got := byID["pod/prod/web-7f8d-a"]; got.ParentExternalID != "deployment/prod/web" || got.Health != model.HealthHealthy || got.ImageDigest != "sha256:abc" {
		t.Fatalf("deployment pod = %+v", got)
	}
	if got := byID["pod/prod/db-0"]; got.ParentExternalID != "statefulset/prod/db" || got.Health != model.HealthUnhealthy || !strings.Contains(got.HealthDetail, "CrashLoopBackOff") {
		t.Fatalf("statefulset pod = %+v", got)
	}
	if got := byID["pod/prod/logs-node-a"]; got.ParentExternalID != "daemonset/prod/logs" {
		t.Fatalf("daemonset pod = %+v", got)
	}

	wantPaths := make([]string, 0, len(responses))
	for path := range responses {
		wantPaths = append(wantPaths, path)
	}
	slices.Sort(wantPaths)
	mu.Lock()
	slices.Sort(requested)
	gotPaths := append([]string(nil), requested...)
	mu.Unlock()
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("requested paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestKubernetesCollectorStopsOnRequiredWorkloadFailure(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var unexpected []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/apps/v1/deployments" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mu.Lock()
		unexpected = append(unexpected, r.URL.Path)
		mu.Unlock()
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	col := &collector{
		cli: &kubeClient{http: server.Client(), base: server.URL, token: "token"},
		cfg: kubeConfig{cluster: "cluster"},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := col.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 403: forbidden") {
		t.Fatalf("Collect error = %v, want bounded 403 response", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unexpected) != 0 {
		t.Fatalf("collector continued after required endpoint failure: %v", unexpected)
	}
}

func TestKubeClientRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"items":`)
	}))
	defer server.Close()

	client := &kubeClient{http: server.Client(), base: server.URL, token: "token"}
	var out podList
	if err := client.get(context.Background(), "/api/v1/pods", &out); err == nil {
		t.Fatal("invalid Kubernetes JSON unexpectedly succeeded")
	}
}
