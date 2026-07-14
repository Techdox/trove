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
	LastStatus           string
}

// ListHosts returns every known host with the owning agent's report interval.
// Host status is derived by callers from LastSeenAt and that interval.
func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id, h.agent_id, h.hostname, h.platform_meta_json, h.last_seen_at, h.last_status,
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
			&h.ID, &h.AgentID, &h.Hostname, &h.MetaJSON, &h.LastSeenAt, &h.LastStatus,
			&h.AgentName, &h.AgentPlatform, &h.AgentIntervalSeconds, &h.AgentLastSeenAt,
		); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// UpdateHostStatus records a host heartbeat transition. The first evaluation
// seeds silently, matching agent status behavior and preventing alert floods
// after startup or migration. host_id is a soft event reference so history
// survives retention pruning while a later same-named host gets fresh alert
// state.
func (s *Store) UpdateHostStatus(
	ctx context.Context,
	hostID, agentID int64,
	hostname, agentName, status string,
) (bool, error) {
	now := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	var last string
	if err := tx.QueryRowContext(ctx,
		`SELECT last_status FROM hosts WHERE id = ?`, hostID).Scan(&last); err != nil {
		return false, fmt.Errorf("read host status: %w", err)
	}
	if last == status {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE hosts SET last_status = ? WHERE id = ?`, status, hostID); err != nil {
		return false, fmt.Errorf("update host status: %w", err)
	}
	if last == "" {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events(kind, host_id, agent_id, hostname, agent, from_state, to_state, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		EventKindHost, hostID, agentID, hostname, agentName, last, status, now,
	); err != nil {
		return false, fmt.Errorf("insert host event: %w", err)
	}
	return true, tx.Commit()
}
