package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/techdox/trove/pkg/model"
)

// MarkServicesStaleForHosts sets health="stale" on all live (non-removed)
// services belonging to the given hosts, and returns the number of rows
// changed. It skips services already marked stale so repeated ticks don't
// churn updated_at (which would prevent pruning). When a host later reports
// again, ApplyReport overwrites health with the freshly reported value, so no
// explicit "un-stale" step is needed.
func (s *Store) MarkServicesStaleForHosts(ctx context.Context, hostIDs []int64) (int64, error) {
	if len(hostIDs) == 0 {
		return 0, nil
	}
	now := s.now().Unix()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hostIDs)), ",")
	args := make([]any, 0, len(hostIDs)+2)
	args = append(args, string(model.HealthStale), now)
	for _, id := range hostIDs {
		args = append(args, id)
	}
	q := fmt.Sprintf(`
		UPDATE services
		   SET health = ?, updated_at = ?
		 WHERE state != '%s'
		   AND health != '%s'
		   AND host_id IN (%s)`, model.StateRemoved, model.HealthStale, placeholders)

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("mark services stale: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
