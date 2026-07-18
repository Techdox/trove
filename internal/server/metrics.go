package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap, err := s.store.MetricsSnapshot(r.Context())
	if err != nil {
		s.log.Error("metrics snapshot", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load metrics")
		return
	}

	var b strings.Builder
	writeMetricHelp(&b, "trove_uptime_seconds", "Seconds since this trove-server process started.")
	writeMetricType(&b, "trove_uptime_seconds", "gauge")
	fmt.Fprintf(&b, "trove_uptime_seconds %.0f\n", time.Since(s.startTime).Seconds())

	writeMetricHelp(&b, "trove_reports_accepted_total", "Reports accepted by this trove-server process since startup.")
	writeMetricType(&b, "trove_reports_accepted_total", "counter")
	fmt.Fprintf(&b, "trove_reports_accepted_total %d\n", s.reportsAccepted.Load())

	workersHealthy := 1
	if s.backgroundHealth != nil && s.backgroundHealth() != nil {
		workersHealthy = 0
	}
	writeMetricHelp(&b, "trove_background_workers_healthy", "Whether all enabled background workers are available (1 healthy, 0 unavailable).")
	writeMetricType(&b, "trove_background_workers_healthy", "gauge")
	fmt.Fprintf(&b, "trove_background_workers_healthy %d\n", workersHealthy)

	writeMetricHelp(&b, "trove_sqlite_database_size_bytes", "SQLite database size based on page_count * page_size.")
	writeMetricType(&b, "trove_sqlite_database_size_bytes", "gauge")
	fmt.Fprintf(&b, "trove_sqlite_database_size_bytes %d\n", snap.DBSizeBytes)

	writeLabeledCounts(&b, "trove_agents", "Registered agents by heartbeat status.", "status", snap.AgentStatusCounts)
	writeLabeledCounts(&b, "trove_services_by_health", "Services by health value.", "health", snap.ServiceHealthCounts)
	writeLabeledCounts(&b, "trove_services_by_state", "Services by runtime state.", "state", snap.ServiceStateCounts)
	writeLabeledCounts(&b, "trove_services_by_kind", "Services by platform kind.", "kind", snap.ServiceKindCounts)
	writeLabeledCounts(&b, "trove_events", "Stored events by kind.", "kind", snap.EventKindCounts)
	writeLabeledCounts(&b, "trove_service_image_freshness", "Services by image freshness verdict.", "status", snap.FreshnessStatusCounts)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func writeMetricHelp(b *strings.Builder, name, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
}

func writeMetricType(b *strings.Builder, name, typ string) {
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
}

func writeLabeledCounts(b *strings.Builder, name, help, label string, counts map[string]int64) {
	writeMetricHelp(b, name, help)
	writeMetricType(b, name, "gauge")
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s{%s=\"%s\"} %d\n", name, label, escapePromLabel(k), counts[k])
	}
}

func escapePromLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}
