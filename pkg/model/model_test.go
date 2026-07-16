package model

import (
	"math"
	"strings"
	"testing"
)

func baseReport() *Report {
	return &Report{
		Agent: ReportAgent{Name: "docker-a", Platform: "docker", Version: "0.1.0"},
		Host:  ReportHost{Hostname: "host-a"},
		Services: []ReportService{{
			ExternalID: "c1", Name: "gitea", Kind: KindContainer,
			State: "running", Health: HealthHealthy,
		}},
	}
}

func TestReportValidate(t *testing.T) {
	if err := baseReport().Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Report)
	}{
		{"missing agent name", func(r *Report) { r.Agent.Name = "" }},
		{"missing platform", func(r *Report) { r.Agent.Platform = "" }},
		{"missing hostname", func(r *Report) { r.Host.Hostname = "" }},
		{"bad host condition", func(r *Report) { r.Host.Condition = "busy" }},
		{"cpu ratio below zero", func(r *Report) {
			r.Host.Metrics = &HostMetrics{CPUUsageRatio: pointer(-0.1)}
		}},
		{"cpu ratio above one", func(r *Report) {
			r.Host.Metrics = &HostMetrics{CPUUsageRatio: pointer(1.1)}
		}},
		{"cpu ratio nan", func(r *Report) {
			r.Host.Metrics = &HostMetrics{CPUUsageRatio: pointer(math.NaN())}
		}},
		{"zero logical CPUs", func(r *Report) {
			r.Host.Metrics = &HostMetrics{CPULogicalCount: pointer(0)}
		}},
		{"negative load", func(r *Report) {
			r.Host.Metrics = &HostMetrics{Load1: pointer(-0.1)}
		}},
		{"memory without capacity", func(r *Report) {
			r.Host.Metrics = &HostMetrics{Memory: &HostResourceUsage{}}
		}},
		{"memory used above capacity", func(r *Report) {
			r.Host.Metrics = &HostMetrics{Memory: &HostResourceUsage{UsedBytes: 11, TotalBytes: 10}}
		}},
		{"missing external id", func(r *Report) { r.Services[0].ExternalID = "" }},
		{"bad kind", func(r *Report) { r.Services[0].Kind = "widget" }},
		{"missing service state", func(r *Report) { r.Services[0].State = " 	" }},
		{"agent may not report removed", func(r *Report) { r.Services[0].State = StateRemoved }},
		{"agent may not report stale", func(r *Report) { r.Services[0].Health = HealthStale }},
		{"duplicate external id", func(r *Report) {
			r.Services = append(r.Services, r.Services[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := baseReport()
			tc.mutate(r)
			if err := r.Validate(); err == nil {
				t.Fatalf("expected validation error for %q", tc.name)
			}
		})
	}
}

func TestReportValidateAcceptsHostConditionAndMetrics(t *testing.T) {
	r := baseReport()
	r.Host.Condition = HostConditionNormal
	r.Host.Metrics = &HostMetrics{
		CPUUsageRatio:   pointer(0.0),
		CPULogicalCount: pointer(8),
		Load1:           pointer(0.0),
		Load5:           pointer(0.25),
		Load15:          pointer(0.5),
		Memory:          &HostResourceUsage{UsedBytes: 0, TotalBytes: 16 * 1024},
		RootDisk:        &HostResourceUsage{UsedBytes: 80, TotalBytes: 100},
		UptimeSeconds:   pointer(uint64(0)),
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("valid host metrics rejected: %v", err)
	}
}

func TestHostMetricsValidationErrorNamesField(t *testing.T) {
	r := baseReport()
	r.Host.Metrics = &HostMetrics{RootDisk: &HostResourceUsage{UsedBytes: 2, TotalBytes: 1}}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "host.metrics.root_disk.used_bytes") {
		t.Fatalf("validation error = %v, want root_disk field path", err)
	}
}

func TestHealthValid(t *testing.T) {
	for _, h := range []Health{HealthHealthy, HealthUnhealthy, HealthUnknown} {
		if !h.Valid() {
			t.Errorf("%q should be a valid agent health", h)
		}
	}
	if HealthStale.Valid() {
		t.Error("stale is server-derived and must not be agent-reportable")
	}
}

func TestHostConditionValid(t *testing.T) {
	for _, condition := range []HostCondition{
		HostConditionNormal, HostConditionWarning, HostConditionCritical, HostConditionUnknown,
	} {
		if !condition.Valid() {
			t.Errorf("%q should be a valid host condition", condition)
		}
	}
	if HostCondition("busy").Valid() {
		t.Error("unrecognized host condition must be rejected")
	}
}

func pointer[T any](v T) *T { return &v }
