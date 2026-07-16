package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func podFromJSON(t *testing.T, s string) pod {
	t.Helper()
	var p pod
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("unmarshal pod: %v", err)
	}
	return p
}

func nodeFromJSON(t *testing.T, s string) kubeNode {
	t.Helper()
	var node kubeNode
	if err := json.Unmarshal([]byte(s), &node); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}
	return node
}

func nodeMetricsFromJSON(t *testing.T, s string) nodeMetricsList {
	t.Helper()
	var metrics nodeMetricsList
	if err := json.Unmarshal([]byte(s), &metrics); err != nil {
		t.Fatalf("unmarshal node metrics: %v", err)
	}
	return metrics
}

func TestPodDetail(t *testing.T) {
	t.Run("healthy has no detail", func(t *testing.T) {
		p := podFromJSON(t, `{"status":{"phase":"Running","containerStatuses":[{"name":"web","ready":true}]}}`)
		if d := podDetail(&p, model.HealthHealthy); d != "" {
			t.Fatalf("healthy detail = %q, want empty", d)
		}
	})

	t.Run("waiting reason (CrashLoopBackOff)", func(t *testing.T) {
		p := podFromJSON(t, `{"status":{"phase":"Running","containerStatuses":[
			{"name":"web","ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff","message":"back-off restarting failed container"}}}]}}`)
		d := podDetail(&p, model.HealthUnhealthy)
		if !strings.Contains(d, "web:") || !strings.Contains(d, "CrashLoopBackOff") || !strings.Contains(d, "back-off restarting") {
			t.Fatalf("waiting detail = %q", d)
		}
	})

	t.Run("terminated reason with exit code", func(t *testing.T) {
		p := podFromJSON(t, `{"status":{"phase":"Failed","containerStatuses":[
			{"name":"job","ready":false,"state":{"terminated":{"reason":"OOMKilled","exitCode":137}}}]}}`)
		d := podDetail(&p, model.HealthUnhealthy)
		if !strings.Contains(d, "job:") || !strings.Contains(d, "OOMKilled") || !strings.Contains(d, "137") {
			t.Fatalf("terminated detail = %q", d)
		}
	})

	t.Run("pod-level reason falls back", func(t *testing.T) {
		p := podFromJSON(t, `{"status":{"phase":"Failed","reason":"Evicted","message":"The node was low on resource: memory."}}`)
		d := podDetail(&p, model.HealthUnhealthy)
		if !strings.Contains(d, "Evicted") || !strings.Contains(d, "low on resource") {
			t.Fatalf("pod-level detail = %q", d)
		}
	})
}

func TestNodeCondition(t *testing.T) {
	ready := nodeFromJSON(t, `{"metadata":{"name":"ready"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	notReady := nodeFromJSON(t, `{"metadata":{"name":"down"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}`)
	unknown := nodeFromJSON(t, `{"metadata":{"name":"unknown"},"status":{"conditions":[{"type":"Ready","status":"Unknown"}]}}`)

	tests := []struct {
		name      string
		nodes     []kubeNode
		condition model.HostCondition
		ready     int
	}{
		{name: "all ready", nodes: []kubeNode{ready, ready}, condition: model.HostConditionNormal, ready: 2},
		{name: "partially ready", nodes: []kubeNode{ready, notReady}, condition: model.HostConditionWarning, ready: 1},
		{name: "none ready", nodes: []kubeNode{notReady, unknown}, condition: model.HostConditionCritical},
		{name: "unknown only", nodes: []kubeNode{unknown}, condition: model.HostConditionUnknown},
		{name: "empty", condition: model.HostConditionUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition, count := nodeCondition(tt.nodes)
			if condition != tt.condition || count != tt.ready {
				t.Fatalf("node condition = (%q, %d), want (%q, %d)", condition, count, tt.condition, tt.ready)
			}
		})
	}
}

func TestAggregateNodeMetrics(t *testing.T) {
	nodes := []kubeNode{
		nodeFromJSON(t, `{"metadata":{"name":"node-a"},"status":{"capacity":{"cpu":"4","memory":"8Gi"}}}`),
		nodeFromJSON(t, `{"metadata":{"name":"node-b"},"status":{"capacity":{"cpu":"8","memory":"16Gi"}}}`),
	}
	usage := nodeMetricsFromJSON(t, `{"items":[
		{"metadata":{"name":"node-a"},"usage":{"cpu":"500m","memory":"2Gi"}},
		{"metadata":{"name":"node-b"},"usage":{"cpu":"1500000000n","memory":"4Gi"}}
	]}`)

	m := aggregateNodeMetrics(nodes, &usage)
	if m == nil || m.CPULogicalCount == nil || *m.CPULogicalCount != 12 || m.CPUUsageRatio == nil ||
		math.Abs(*m.CPUUsageRatio-(2.0/12.0)) > 0.0001 || m.Memory == nil ||
		m.Memory.UsedBytes != 6*(1<<30) || m.Memory.TotalBytes != 24*(1<<30) {
		t.Fatalf("aggregate metrics = %+v", m)
	}

	capacityOnly := aggregateNodeMetrics(nodes, nil)
	if capacityOnly == nil || capacityOnly.CPULogicalCount == nil || *capacityOnly.CPULogicalCount != 12 ||
		capacityOnly.CPUUsageRatio != nil || capacityOnly.Memory != nil {
		t.Fatalf("capacity-only metrics = %+v", capacityOnly)
	}
}

func TestKubernetesQuantityParsing(t *testing.T) {
	for raw, want := range map[string]float64{"250m": 0.25, "123000000n": 0.123, "2": 2} {
		got, ok := parseCPUQuantity(raw)
		if !ok || math.Abs(got-want) > 0.000001 {
			t.Errorf("cpu quantity %q = (%v, %v), want %v", raw, got, ok, want)
		}
	}
	for raw, want := range map[string]uint64{"512Ki": 512 << 10, "2Gi": 2 << 30, "100M": 100_000_000} {
		got, ok := parseByteQuantity(raw)
		if !ok || got != want {
			t.Errorf("byte quantity %q = (%d, %v), want %d", raw, got, ok, want)
		}
	}
}
