package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
)

var backupPermissionMu sync.Mutex

// Backup writes a consistent SQLite backup to dst using SQLite's online
// VACUUM INTO mechanism. SQLite refuses to overwrite an existing output file;
// callers should still preflight paths so the CLI can return a clear error.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if dst == "" {
		return fmt.Errorf("backup path is required")
	}
	// VACUUM INTO insists on creating a new destination itself, so pre-creating
	// a 0600 file is not possible. The backup command is a single-purpose
	// process; hold a package lock while applying a restrictive process umask,
	// then enforce the final mode as defense in depth.
	backupPermissionMu.Lock()
	defer backupPermissionMu.Unlock()
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("backup database to %q: %w", dst, err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return fmt.Errorf("secure backup permissions for %q: %w", dst, err)
	}
	return nil
}
