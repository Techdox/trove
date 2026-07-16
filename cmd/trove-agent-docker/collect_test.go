package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

type stubMetricSampler struct {
	metrics *model.HostMetrics
	err     error
}

func (s stubMetricSampler) Collect() (*model.HostMetrics, error) { return s.metrics, s.err }

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

func TestCollectReportsDockerHostConditionAndMetrics(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/containers/json":
			_, _ = io.WriteString(w, `[]`)
		case "/info":
			_, _ = io.WriteString(w, `{"Name":"docker-host","ServerVersion":"28.1","NCPU":8,"OperatingSystem":"Debian 12","KernelVersion":"6.8.0"}`)
		case "/version":
			_, _ = io.WriteString(w, `{"Version":"28.1","ApiVersion":"1.49","Os":"linux","Arch":"amd64"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	ratio := 0.25
	col := &collector{
		cli: &dockerClient{http: api.Client(), base: api.URL},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics: stubMetricSampler{metrics: &model.HostMetrics{
			CPUUsageRatio: &ratio,
		}},
	}
	snaps, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Host.Hostname != "docker-host" ||
		snaps[0].Host.Condition != model.HostConditionNormal || snaps[0].Host.Metrics == nil ||
		snaps[0].Host.Metrics.CPULogicalCount == nil || *snaps[0].Host.Metrics.CPULogicalCount != 8 ||
		snaps[0].Host.Metrics.CPUUsageRatio == nil || *snaps[0].Host.Metrics.CPUUsageRatio != 0.25 {
		t.Fatalf("docker snapshot = %+v", snaps)
	}
	if snaps[0].Host.Meta["docker.os_name"] != "Debian 12" || snaps[0].Host.Meta["docker.kernel"] != "6.8.0" {
		t.Fatalf("docker metadata = %+v", snaps[0].Host.Meta)
	}
}

func TestDockerClientOnlyTreatsUnixSocketAsLocalHost(t *testing.T) {
	local, err := newDockerClient("unix:///var/run/docker.sock")
	if err != nil {
		t.Fatalf("local client: %v", err)
	}
	remote, err := newDockerClient("tcp://docker.example:2375")
	if err != nil {
		t.Fatalf("remote client: %v", err)
	}
	if !local.localHost || remote.localHost {
		t.Fatalf("local flags = unix:%v remote:%v", local.localHost, remote.localHost)
	}
}
