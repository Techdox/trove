package main

import (
	"encoding/json"
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
