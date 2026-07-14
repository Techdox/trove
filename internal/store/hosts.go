package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Host is the heartbeat state needed to evaluate a host independently from
// other hosts reported by the same agent.
type Host struct {
	ID                   int64
	AgentID              int64
	Hostname             string
	MetaJSON             string
	AgentName            string
	AgentPlatform        string
	AgentIntervalSeconds int
	LastSeenAt           sql.NullInt64
	AgentLastSeenAt      sql.NullInt64
}

// ListHosts returns every known host with the owning agent's report interval.
// Host status is derived by callers from LastSeenAt and that interval.
func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id, h.agent_id, h.hostname, h.platform_meta_json, h.last_seen_at,
		       a.name, a.platform, a.report_interval_seconds, a.last_seen_at
		  FROM hosts h
		  JOIN agents a ON a.id = h.agent_id
		 ORDER BY a.name, h.hostname`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(
			&h.ID, &h.AgentID, &h.Hostname, &h.MetaJSON, &h.LastSeenAt,
			&h.AgentName, &h.AgentPlatform, &h.AgentIntervalSeconds, &h.AgentLastSeenAt,
		); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
