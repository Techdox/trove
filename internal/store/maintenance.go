package store

import (
	"context"
	"fmt"

	"github.com/techdox/trove/pkg/model"
)

// PruneStats reports what a maintenance pass removed.
type PruneStats struct {
	Events          int64
	RemovedServices int64
}

// Prune deletes events older than eventRetention and hard-deletes services
// that have been soft-removed for longer than removedRetention (both given in
// seconds). It nulls any child's parent_id pointing at a to-be-pruned parent
// first, so no dangling references are left (referential integrity here is
// managed in code, not FKs — see migration 0003).
//
// Pruning runs on the server's maintenance ticker, deliberately off the
// report-ingest write path (ROADMAP decision D1).
func (s *Store) Prune(ctx context.Context, eventRetentionSecs, removedRetentionSecs int64) (PruneStats, error) {
	now := s.now().Unix()
	eventCutoff := now - eventRetentionSecs
	removedCutoff := now - removedRetentionSecs

	var stats PruneStats

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, eventCutoff)
	if err != nil {
		return stats, fmt.Errorf("prune events: %w", err)
	}
	stats.Events, _ = res.RowsAffected()

	if _, err := tx.ExecContext(ctx,
		`UPDATE services SET parent_id = NULL
		  WHERE parent_id IN (SELECT id FROM services WHERE state = ? AND updated_at < ?)`,
		model.StateRemoved, removedCutoff,
	); err != nil {
		return stats, fmt.Errorf("null dangling parents: %w", err)
	}
	res, err = tx.ExecContext(ctx,
		`DELETE FROM services WHERE state = ? AND updated_at < ?`, model.StateRemoved, removedCutoff)
	if err != nil {
		return stats, fmt.Errorf("prune removed services: %w", err)
	}
	stats.RemovedServices, _ = res.RowsAffected()

	return stats, tx.Commit()
}
