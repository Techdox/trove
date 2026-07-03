package store

import (
	"context"
	"fmt"
	"strings"

	"trove/pkg/model"
)

// MarkServicesStaleForAgents sets health="stale" on all live (non-removed)
// services belonging to the given agents, and returns the number of rows
// changed. It skips services already marked stale so repeated ticks don't
// churn updated_at (which would prevent pruning). When an agent later reports
// again, ApplyReport overwrites health with the freshly reported value, so no
// explicit "un-stale" step is needed.
func (s *Store) MarkServicesStaleForAgents(ctx context.Context, agentIDs []int64) (int64, error) {
	if len(agentIDs) == 0 {
		return 0, nil
	}
	now := s.now().Unix()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(agentIDs)), ",")
	args := make([]any, 0, len(agentIDs)+2)
	args = append(args, string(model.HealthStale), now)
	for _, id := range agentIDs {
		args = append(args, id)
	}
	q := fmt.Sprintf(`
		UPDATE services
		   SET health = ?, updated_at = ?
		 WHERE state != '%s'
		   AND health != '%s'
		   AND host_id IN (
		       SELECT id FROM hosts WHERE agent_id IN (%s)
		   )`, model.StateRemoved, model.HealthStale, placeholders)

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("mark services stale: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
