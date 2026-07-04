package store

import (
	"context"
	"fmt"
)

// ImagesDueForCheck returns distinct image references used by live services
// with a captured running digest that are due for a freshness check (never
// checked, or past next_check_at). Limited so a single ticker pass can't
// stampede a registry.
func (s *Store) ImagesDueForCheck(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	now := s.now().Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT s.image
		  FROM services s
		  LEFT JOIN image_checks c ON c.image = s.image
		 WHERE s.image != '' AND s.image_digest != '' AND s.state != 'removed'
		   AND (c.image IS NULL OR c.next_check_at <= ?)
		 LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("images due: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var img string
		if err := rows.Scan(&img); err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// RecordImageDigest stores a successful freshness result: the resolved latest
// digest and the next scheduled check.
func (s *Store) RecordImageDigest(ctx context.Context, image, latestDigest string, nextCheckAt int64) error {
	now := s.now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO image_checks(image, latest_digest, status, error, checked_at, next_check_at)
		VALUES (?, ?, 'ok', '', ?, ?)
		ON CONFLICT(image) DO UPDATE SET
			latest_digest = excluded.latest_digest,
			status        = 'ok',
			error         = '',
			checked_at    = excluded.checked_at,
			next_check_at = excluded.next_check_at`,
		image, latestDigest, now, nextCheckAt)
	if err != nil {
		return fmt.Errorf("record image digest: %w", err)
	}
	return nil
}

// RecordImageError records a failed check and reschedules it, deliberately
// leaving any previously-resolved latest_digest intact so a transient registry
// error doesn't blank out known-good freshness data.
func (s *Store) RecordImageError(ctx context.Context, image, errMsg string, nextCheckAt int64) error {
	now := s.now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO image_checks(image, latest_digest, status, error, checked_at, next_check_at)
		VALUES (?, '', 'error', ?, ?, ?)
		ON CONFLICT(image) DO UPDATE SET
			status        = 'error',
			error         = excluded.error,
			checked_at    = excluded.checked_at,
			next_check_at = excluded.next_check_at`,
		image, errMsg, now, nextCheckAt)
	if err != nil {
		return fmt.Errorf("record image error: %w", err)
	}
	return nil
}
