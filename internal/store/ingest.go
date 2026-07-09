package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/techdox/trove/pkg/model"
)

// ApplyReport ingests a full-state report from the given agent inside a single
// transaction. It is idempotent: applying the same report twice yields the
// same state and records no spurious events.
//
// Semantics:
//   - the agent's last_seen/platform/version/interval are refreshed;
//   - each reported service is upserted (correlated by host + external_id);
//   - a service whose state changed since last report records an event;
//   - services previously seen but absent from this report are soft-removed
//     (state="removed") and record a transition event once.
//
// Retention pruning is NOT done here — see Prune (maintenance.go).
func (s *Store) ApplyReport(ctx context.Context, agentID int64, r *model.Report) error {
	now := s.now().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// 1. Refresh agent heartbeat + metadata, and resolve the agent's display
	// name (the token name, not the reported one) for event denormalization.
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents
		    SET last_seen_at = ?, platform = ?, version = ?, report_interval_seconds = ?
		  WHERE id = ?`,
		now, r.Agent.Platform, r.Agent.Version, r.Agent.IntervalSeconds, agentID,
	); err != nil {
		return fmt.Errorf("update agent heartbeat: %w", err)
	}
	var agentName string
	if err := tx.QueryRowContext(ctx,
		`SELECT name FROM agents WHERE id = ?`, agentID).Scan(&agentName); err != nil {
		return fmt.Errorf("resolve agent name: %w", err)
	}
	// evt captures the denormalized context shared by every event this report
	// can produce.
	evt := func(kind string, serviceID int64, svcName, from, to string) error {
		return insertEvent(ctx, tx, kind, serviceID, svcName, r.Host.Hostname, agentName, from, to, now)
	}

	// 2. Upsert host, resolve host_id.
	metaJSON := mustJSONObject(r.Host.Meta)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hosts(agent_id, hostname, platform_meta_json) VALUES (?, ?, ?)
		 ON CONFLICT(agent_id, hostname) DO UPDATE SET platform_meta_json = excluded.platform_meta_json`,
		agentID, r.Host.Hostname, metaJSON,
	); err != nil {
		return fmt.Errorf("upsert host: %w", err)
	}
	var hostID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM hosts WHERE agent_id = ? AND hostname = ?`, agentID, r.Host.Hostname,
	).Scan(&hostID); err != nil {
		return fmt.Errorf("resolve host id: %w", err)
	}

	// 3. Load current services for this host (id + state), keyed by external_id.
	type existing struct {
		id     int64
		name   string
		state  string
		health string
	}
	current := map[string]existing{}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, external_id, name, state, health FROM services WHERE host_id = ?`, hostID)
	if err != nil {
		return fmt.Errorf("load services: %w", err)
	}
	for rows.Next() {
		var id int64
		var extID, name, state, health string
		if err := rows.Scan(&id, &extID, &name, &state, &health); err != nil {
			rows.Close()
			return err
		}
		current[extID] = existing{id: id, name: name, state: state, health: health}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 4. Upsert each reported service, recording the resulting row id per
	// external_id so the parent-resolution pass (step 5) and soft-removal
	// (step 6) can use it.
	idByExtID := make(map[string]int64, len(r.Services))
	for i := range r.Services {
		svc := &r.Services[i]
		portsJSON := mustJSONArray(svc.Ports)
		labelsJSON := mustJSONObject(svc.Labels)

		if ex, ok := current[svc.ExternalID]; ok {
			idByExtID[svc.ExternalID] = ex.id
			if ex.state != svc.State {
				if err := evt(EventKindState, ex.id, svc.Name, ex.state, svc.State); err != nil {
					return err
				}
			}
			if ex.health != string(svc.Health) {
				if err := evt(EventKindHealth, ex.id, svc.Name, ex.health, string(svc.Health)); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE services
				    SET name = ?, kind = ?, image = ?, image_digest = ?, state = ?, health = ?,
				        health_detail = ?, ports_json = ?, labels_json = ?, last_seen_at = ?, updated_at = ?
				  WHERE id = ?`,
				svc.Name, string(svc.Kind), svc.Image, svc.ImageDigest, svc.State, string(svc.Health),
				svc.HealthDetail, portsJSON, labelsJSON, now, now, ex.id,
			); err != nil {
				return fmt.Errorf("update service %q: %w", svc.ExternalID, err)
			}
			continue
		}

		// New service.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO services
			   (host_id, external_id, name, kind, image, image_digest, state, health,
			    health_detail, ports_json, labels_json, first_seen_at, last_seen_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hostID, svc.ExternalID, svc.Name, string(svc.Kind), svc.Image, svc.ImageDigest,
			svc.State, string(svc.Health), svc.HealthDetail, portsJSON, labelsJSON, now, now, now,
		)
		if err != nil {
			return fmt.Errorf("insert service %q: %w", svc.ExternalID, err)
		}
		newID, _ := res.LastInsertId()
		idByExtID[svc.ExternalID] = newID
		// Record the appearance so it shows in the event feed.
		if err := evt(EventKindState, newID, svc.Name, "", svc.State); err != nil {
			return err
		}
	}

	// 5. Resolve parent/child links. Both parent and child are present in the
	// Resolve parent links after all services are upserted (parents and children
	// may appear in any order in the report). Touch every reported service so a
	// child that later becomes standalone has its old parent_id cleared.
	for i := range r.Services {
		svc := &r.Services[i]
		childID, ok := idByExtID[svc.ExternalID]
		if !ok {
			continue
		}

		var parentID sql.NullInt64
		if svc.ParentExternalID != "" {
			pid, ok := idByExtID[svc.ParentExternalID]
			if !ok {
				// Unknown parent in this host snapshot. Leave parent_id unchanged
				// rather than guessing across hosts/agents.
				continue
			}
			parentID = sql.NullInt64{Int64: pid, Valid: true}
		}

		if _, err := tx.ExecContext(ctx, `UPDATE services SET parent_id = ? WHERE id = ?`, parentID, childID); err != nil {
			return fmt.Errorf("link service %q to parent %q: %w", svc.ExternalID, svc.ParentExternalID, err)
		}
	}

	// 6. Soft-remove services absent from this report (once).
	for extID, ex := range current {
		if _, present := idByExtID[extID]; present {
			continue
		}
		if ex.state == model.StateRemoved {
			continue // already removed; leave updated_at so it can age out
		}
		// A removal is a state event only; the accompanying health reset to
		// "unknown" would just be noise.
		if err := evt(EventKindState, ex.id, ex.name, ex.state, model.StateRemoved); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE services SET state = ?, health = ?, updated_at = ? WHERE id = ?`,
			model.StateRemoved, string(model.HealthUnknown), now, ex.id,
		); err != nil {
			return fmt.Errorf("soft-remove service %q: %w", extID, err)
		}
	}

	// Pruning of old events and long-removed services happens on the server's
	// maintenance ticker (store.Prune), not on the ingest write path.
	return tx.Commit()
}

// Event kinds. The events table carries three streams that the dashboard feed
// and the alert engine both consume.
const (
	EventKindState  = "state"  // service platform-state transition
	EventKindHealth = "health" // service health transition
	EventKindAgent  = "agent"  // agent heartbeat-status transition
)

// insertEvent records a service-scoped event with its display context
// denormalized, so the event stays renderable after the service is pruned.
func insertEvent(ctx context.Context, tx *sql.Tx, kind string, serviceID int64, svcName, hostname, agentName, from, to string, at int64) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events(kind, service_id, service, hostname, agent, from_state, to_state, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		kind, serviceID, svcName, hostname, agentName, from, to, at,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// mustJSONObject marshals a string map to JSON, guaranteeing a non-null object
// literal ("{}" when empty) so the column never holds SQL NULL or "null".
func mustJSONObject(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// mustJSONArray marshals ports to a JSON array, guaranteeing "[]" when empty.
func mustJSONArray(p []model.Port) string {
	if len(p) == 0 {
		return "[]"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "[]"
	}
	return string(b)
}
