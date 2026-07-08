package store

import (
	"context"
	"fmt"
)

// MetricsSnapshot is the aggregate state exposed by the Prometheus /metrics
// endpoint. It intentionally contains counts only — no labels that could leak
// service names, hosts, or images into a metrics backend.
type MetricsSnapshot struct {
	AgentStatusCounts     map[string]int64
	ServiceHealthCounts   map[string]int64
	ServiceStateCounts    map[string]int64
	ServiceKindCounts     map[string]int64
	EventKindCounts       map[string]int64
	FreshnessStatusCounts map[string]int64
	DBSizeBytes           int64
}

// MetricsSnapshot returns aggregate counts for Trove's own observability.
func (s *Store) MetricsSnapshot(ctx context.Context) (MetricsSnapshot, error) {
	out := MetricsSnapshot{}
	var err error
	if out.AgentStatusCounts, err = s.groupCounts(ctx, `SELECT COALESCE(NULLIF(last_status, ''), 'unknown'), COUNT(*) FROM agents GROUP BY 1`); err != nil {
		return out, fmt.Errorf("agent status counts: %w", err)
	}
	if out.ServiceHealthCounts, err = s.groupCounts(ctx, `SELECT COALESCE(NULLIF(health, ''), 'unknown'), COUNT(*) FROM services GROUP BY 1`); err != nil {
		return out, fmt.Errorf("service health counts: %w", err)
	}
	if out.ServiceStateCounts, err = s.groupCounts(ctx, `SELECT COALESCE(NULLIF(state, ''), 'unknown'), COUNT(*) FROM services GROUP BY 1`); err != nil {
		return out, fmt.Errorf("service state counts: %w", err)
	}
	if out.ServiceKindCounts, err = s.groupCounts(ctx, `SELECT COALESCE(NULLIF(kind, ''), 'unknown'), COUNT(*) FROM services GROUP BY 1`); err != nil {
		return out, fmt.Errorf("service kind counts: %w", err)
	}
	if out.EventKindCounts, err = s.groupCounts(ctx, `SELECT COALESCE(NULLIF(kind, ''), 'unknown'), COUNT(*) FROM events GROUP BY 1`); err != nil {
		return out, fmt.Errorf("event kind counts: %w", err)
	}
	if out.FreshnessStatusCounts, err = s.groupCounts(ctx, `
		SELECT CASE
			WHEN c.status IS NULL OR c.status <> 'ok' OR c.latest_digest = '' OR s.image_digest = '' THEN 'unknown'
			WHEN c.latest_digest = s.image_digest THEN 'current'
			ELSE 'outdated'
		END AS freshness, COUNT(*)
		FROM services s
		LEFT JOIN image_checks c ON c.image = s.image
		GROUP BY freshness`); err != nil {
		return out, fmt.Errorf("freshness counts: %w", err)
	}

	var pages, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return out, fmt.Errorf("page_count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return out, fmt.Errorf("page_size: %w", err)
	}
	out.DBSizeBytes = pages * pageSize
	return out, nil
}

func (s *Store) groupCounts(ctx context.Context, query string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		out[key] = count
	}
	return out, rows.Err()
}
