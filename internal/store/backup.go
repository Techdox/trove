package store

import (
	"context"
	"fmt"
)

// Backup writes a consistent SQLite backup to dst using SQLite's online
// VACUUM INTO mechanism. SQLite refuses to overwrite an existing output file;
// callers should still preflight paths so the CLI can return a clear error.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if dst == "" {
		return fmt.Errorf("backup path is required")
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("backup database to %q: %w", dst, err)
	}
	return nil
}
