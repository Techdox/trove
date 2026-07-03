package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"trove/pkg/model"
)

// retentionWindow bounds the event log and how long soft-removed services
// linger before being pruned. Phase 1 keeps no unbounded history.
const retentionWindow = 24 * time.Hour

// ApplyReport ingests a full-state report from the given agent inside a single
// transaction. It is idempotent: applying the same report twice yields the
// same state and records no spurious events.
//
// Semantics:
//   - the agent's last_seen/platform/version/interval are refreshed;
//   - each reported service is upserted (correlated by host + external_id);
//   - a service whose state changed since last report records an event;
//   - services previously seen but absent from this report are soft-removed
//     (state="removed") and record a transition event once;
//   - events older than 24h and services removed for over 24h are pruned.
func (s *Store) ApplyReport(ctx context.Context, agentID int64, r *model.Report) error {
	now := s.now().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// 1. Refresh agent heartbeat + metadata.
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents
		    SET last_seen_at = ?, platform = ?, version = ?, report_interval_seconds = ?
		  WHERE id = ?`,
		now, r.Agent.Platform, r.Agent.Version, r.Agent.IntervalSeconds, agentID,
	); err != nil {
		return fmt.Errorf("update agent heartbeat: %w", err)
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
		id    int64
		state string
	}
	current := map[string]existing{}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, external_id, state FROM services WHERE host_id = ?`, hostID)
	if err != nil {
		return fmt.Errorf("load services: %w", err)
	}
	for rows.Next() {
		var id int64
		var extID, state string
		if err := rows.Scan(&id, &extID, &state); err != nil {
			rows.Close()
			return err
		}
		current[extID] = existing{id: id, state: state}
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
				if err := insertEvent(ctx, tx, ex.id, ex.state, svc.State, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE services
				    SET name = ?, kind = ?, image = ?, image_digest = ?, state = ?, health = ?,
				        ports_json = ?, labels_json = ?, last_seen_at = ?, updated_at = ?
				  WHERE id = ?`,
				svc.Name, string(svc.Kind), svc.Image, svc.ImageDigest, svc.State, string(svc.Health),
				portsJSON, labelsJSON, now, now, ex.id,
			); err != nil {
				return fmt.Errorf("update service %q: %w", svc.ExternalID, err)
			}
			continue
		}

		// New service.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO services
			   (host_id, external_id, name, kind, image, image_digest, state, health,
			    ports_json, labels_json, first_seen_at, last_seen_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hostID, svc.ExternalID, svc.Name, string(svc.Kind), svc.Image, svc.ImageDigest,
			svc.State, string(svc.Health), portsJSON, labelsJSON, now, now, now,
		)
		if err != nil {
			return fmt.Errorf("insert service %q: %w", svc.ExternalID, err)
		}
		newID, _ := res.LastInsertId()
		idByExtID[svc.ExternalID] = newID
		// Record the appearance so it shows in the 24h event feed.
		if err := insertEvent(ctx, tx, newID, "", svc.State, now); err != nil {
			return err
		}
	}

	// 5. Resolve parent/child links. Both parent and child are present in the
	// same full-state report, so all ids are known by now regardless of order.
	// Only touch services that report a parent (leaves standalone rows' NULL
	// parent_id untouched, avoiding needless writes).
	for i := range r.Services {
		svc := &r.Services[i]
		if svc.ParentExternalID == "" {
			continue
		}
		childID, ok := idByExtID[svc.ExternalID]
		if !ok {
			continue
		}
		var parentID sql.NullInt64
		if pid, ok := idByExtID[svc.ParentExternalID]; ok {
			parentID = sql.NullInt64{Int64: pid, Valid: true}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE services SET parent_id = ? WHERE id = ?`, parentID, childID,
		); err != nil {
			return fmt.Errorf("link service %q to parent: %w", svc.ExternalID, err)
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
		if err := insertEvent(ctx, tx, ex.id, ex.state, model.StateRemoved, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE services SET state = ?, health = ?, updated_at = ? WHERE id = ?`,
			model.StateRemoved, string(model.HealthUnknown), now, ex.id,
		); err != nil {
			return fmt.Errorf("soft-remove service %q: %w", extID, err)
		}
	}

	// 7. Prune old events and long-removed services.
	cutoff := now - int64(retentionWindow.Seconds())
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, cutoff); err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	// Null out any child whose parent is about to be pruned, so we never leave
	// a dangling parent_id (we manage this in code rather than via an FK).
	if _, err := tx.ExecContext(ctx,
		`UPDATE services SET parent_id = NULL
		  WHERE parent_id IN (SELECT id FROM services WHERE state = ? AND updated_at < ?)`,
		model.StateRemoved, cutoff,
	); err != nil {
		return fmt.Errorf("null dangling parents: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM services WHERE state = ? AND updated_at < ?`, model.StateRemoved, cutoff,
	); err != nil {
		return fmt.Errorf("prune removed services: %w", err)
	}

	return tx.Commit()
}

func insertEvent(ctx context.Context, tx *sql.Tx, serviceID int64, from, to string, at int64) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events(service_id, from_state, to_state, at) VALUES (?, ?, ?, ?)`,
		serviceID, from, to, at,
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
