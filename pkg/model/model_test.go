package model

import "testing"

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
