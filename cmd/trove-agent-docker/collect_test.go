package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func inspectFromJSON(t *testing.T, s string) dockerInspect {
	t.Helper()
	var insp dockerInspect
	if err := json.Unmarshal([]byte(s), &insp); err != nil {
		t.Fatalf("unmarshal inspect: %v", err)
	}
	return insp
}

func TestHealthDetail(t *testing.T) {
	t.Run("healthy has no detail", func(t *testing.T) {
		insp := inspectFromJSON(t, `{"State":{"Health":{"Status":"healthy"}}}`)
		if d := healthDetail("running", insp, model.HealthHealthy); d != "" {
			t.Fatalf("healthy detail = %q, want empty", d)
		}
	})

	t.Run("failing healthcheck reports exit code + collapsed output", func(t *testing.T) {
		insp := inspectFromJSON(t, `{"State":{"Health":{"Status":"unhealthy","FailingStreak":3,
			"Log":[{"ExitCode":0,"Output":"ok"},{"ExitCode":1,"Output":"curl: (7)\n  connection refused\n"}]}}}`)
		d := healthDetail("running", insp, model.HealthUnhealthy)
		if !strings.Contains(d, "exit 1") || !strings.Contains(d, "curl: (7) connection refused") {
			t.Fatalf("healthcheck detail = %q", d)
		}
		if strings.Contains(d, "\n") {
			t.Fatalf("detail should be single-line, got %q", d)
		}
	})

	t.Run("stopped-but-should-run reports exit code + error", func(t *testing.T) {
		insp := inspectFromJSON(t, `{"State":{"ExitCode":137,"Error":"OOMKilled"},
			"HostConfig":{"RestartPolicy":{"Name":"always"}}}`)
		d := healthDetail("exited", insp, model.HealthUnhealthy)
		if !strings.Contains(d, "exited") || !strings.Contains(d, "137") || !strings.Contains(d, "OOMKilled") {
			t.Fatalf("stopped detail = %q", d)
		}
	})

	t.Run("long output is truncated", func(t *testing.T) {
		long := strings.Repeat("x", 500)
		insp := inspectFromJSON(t, `{"State":{"Health":{"Status":"unhealthy","Log":[{"ExitCode":1,"Output":"`+long+`"}]}}}`)
		d := healthDetail("running", insp, model.HealthUnhealthy)
		if len(d) > 360 { // 300 cap + prefix + ellipsis
			t.Fatalf("detail not truncated: len %d", len(d))
		}
		if !strings.HasSuffix(d, "…") {
			t.Fatalf("truncated detail should end with ellipsis: %q", d)
		}
	})
}
