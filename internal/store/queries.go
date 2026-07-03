package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ServiceRow is a fully-joined service record: the service plus its host and
// owning agent. The dashboard groups these by host; the API layer overlays
// derived staleness using the agent heartbeat fields.
type ServiceRow struct {
	ID          int64
	ExternalID  string
	Name        string
	Kind        string
	Image       string
	ImageDigest string
	State       string
	Health      string
	PortsJSON   string
	LabelsJSON  string
	FirstSeenAt int64
	LastSeenAt  int64
	UpdatedAt   int64

	HostID       int64
	Hostname     string
	HostMetaJSON string

	AgentID              int64
	AgentName            string
	AgentPlatform        string
	AgentIntervalSeconds int
	AgentLastSeenAt      sql.NullInt64

	// ParentExternalID links a child instance (e.g. a K8s pod) to its parent
	// workload's external_id within the same host. Empty for standalone
	// services (containers, VMs).
	ParentExternalID sql.NullString

	// Freshness cache (Phase 2), left-joined from image_checks. Nullable when
	// the image has not been checked yet.
	LatestDigest       sql.NullString
	FreshnessStatus    sql.NullString
	FreshnessCheckedAt sql.NullInt64
}

// ListServices returns every service joined to its host and agent, ordered for
// stable grouping (agent, host, name).
func (s *Store) ListServices(ctx context.Context) ([]ServiceRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.external_id, s.name, s.kind, s.image, s.image_digest, s.state, s.health,
		       s.ports_json, s.labels_json, s.first_seen_at, s.last_seen_at, s.updated_at,
		       h.id, h.hostname, h.platform_meta_json,
		       a.id, a.name, a.platform, a.report_interval_seconds, a.last_seen_at,
		       p.external_id AS parent_external_id,
		       c.latest_digest, c.status, c.checked_at
		  FROM services s
		  JOIN hosts h  ON h.id = s.host_id
		  JOIN agents a ON a.id = h.agent_id
		  LEFT JOIN services p     ON p.id = s.parent_id
		  LEFT JOIN image_checks c ON c.image = s.image
		 ORDER BY a.name, h.hostname, s.name`)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()

	var out []ServiceRow
	for rows.Next() {
		var r ServiceRow
		if err := rows.Scan(
			&r.ID, &r.ExternalID, &r.Name, &r.Kind, &r.Image, &r.ImageDigest, &r.State, &r.Health,
			&r.PortsJSON, &r.LabelsJSON, &r.FirstSeenAt, &r.LastSeenAt, &r.UpdatedAt,
			&r.HostID, &r.Hostname, &r.HostMetaJSON,
			&r.AgentID, &r.AgentName, &r.AgentPlatform, &r.AgentIntervalSeconds, &r.AgentLastSeenAt,
			&r.ParentExternalID,
			&r.LatestDigest, &r.FreshnessStatus, &r.FreshnessCheckedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventRow is a state-change event joined to its service and host, for the
// recent-events feed.
type EventRow struct {
	ID        int64
	FromState string
	ToState   string
	At        int64
	Service   string
	Hostname  string
}

// RecentEvents returns the most recent state-change events, newest first.
func (s *Store) RecentEvents(ctx context.Context, limit int) ([]EventRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.from_state, e.to_state, e.at, s.name, h.hostname
		  FROM events e
		  JOIN services s ON s.id = e.service_id
		  JOIN hosts h    ON h.id = s.host_id
		 ORDER BY e.at DESC, e.id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.ID, &e.FromState, &e.ToState, &e.At, &e.Service, &e.Hostname); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
