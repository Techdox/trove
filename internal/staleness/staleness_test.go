package staleness

import (
	"testing"
	"time"
)

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	cases := []struct {
		name     string
		lastSeen *time.Time
		interval int
		want     Status
	}{
		{"never reported", nil, 0, StatusUnknown},
		{"fresh, default interval", ago(20 * time.Second), 0, StatusOK},
		{"just under stale (90s)", ago(89 * time.Second), 0, StatusOK},
		{"just over stale", ago(91 * time.Second), 0, StatusStale},
		{"just under offline (300s)", ago(299 * time.Second), 0, StatusStale},
		{"just over offline", ago(301 * time.Second), 0, StatusOffline},
		// 60s interval => stale after 180s, offline after 600s.
		{"slow agent still ok at 100s", ago(100 * time.Second), 60, StatusOK},
		{"slow agent stale at 200s", ago(200 * time.Second), 60, StatusStale},
		{"slow agent offline at 700s", ago(700 * time.Second), 60, StatusOffline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.lastSeen, tc.interval, now); got != tc.want {
				t.Fatalf("Evaluate = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStaleOrWorse(t *testing.T) {
	if StaleOrWorse(StatusOK) || StaleOrWorse(StatusUnknown) {
		t.Fatal("ok/unknown should not be stale-or-worse")
	}
	if !StaleOrWorse(StatusStale) || !StaleOrWorse(StatusOffline) {
		t.Fatal("stale/offline should be stale-or-worse")
	}
}
