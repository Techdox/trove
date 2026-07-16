package server

import (
	"strings"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func TestHealthDetailsDisabledByDefault(t *testing.T) {
	t.Setenv("TROVE_HEALTH_DETAILS_ENABLED", "")
	if LoadHealthDetailsEnabledFromEnv() {
		t.Fatal("health details must be disabled by default")
	}
	srv := New(nil, nil)
	report := &model.Report{Services: []model.ReportService{{HealthDetail: "token=secret"}}}
	srv.filterHealthDetails(report)
	if report.Services[0].HealthDetail != "" {
		t.Fatalf("disabled health detail retained: %q", report.Services[0].HealthDetail)
	}
	if got := srv.exposedHealthDetail("old database detail"); got != "" {
		t.Fatalf("disabled health detail exposed from old data: %q", got)
	}
}

func TestEnabledHealthDetailsAreRedactedAndBounded(t *testing.T) {
	srv := New(nil, nil)
	srv.ConfigureHealthDetails(true)
	report := &model.Report{Services: []model.ReportService{{
		HealthDetail: "request failed\nAuthorization: Bearer abc.def token=topsecret password=hunter2 " + strings.Repeat("x", 700),
	}}}
	srv.filterHealthDetails(report)
	got := report.Services[0].HealthDetail
	for _, secret := range []string{"abc.def", "topsecret", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Fatalf("health detail leaked %q: %q", secret, got)
		}
	}
	if strings.Contains(got, "\n") || len([]rune(got)) > maxStoredHealthDetailRunes {
		t.Fatalf("health detail not normalized/bounded: %q", got)
	}
}
