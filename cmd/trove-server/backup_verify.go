package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/techdox/trove/internal/store"
)

// runBackupVerify checks an existing Trove backup without creating, migrating,
// or modifying it. The digest comparison detects changes while the command is
// running, including accidental writes by this process.
func runBackupVerify(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: trove-server backup verify <path>")
	}
	path := args[0]
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat backup path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup path %q must be a regular file", path)
	}

	before, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("hash backup before verification: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	migrations, err := inspectBackupReadOnly(ctx, path)
	if err != nil {
		return err
	}

	after, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("hash backup after verification: %w", err)
	}
	if before != after {
		return errors.New("backup changed during read-only verification")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	fmt.Println("Trove backup verify")
	fmt.Printf("backup: %s (%d bytes)\n", absPath, info.Size())
	fmt.Printf("sha256: %x\n", before)
	fmt.Println("database: ok (read-only integrity check)")
	fmt.Printf("migrations: %d applied, %d pending, %d unknown\n", len(migrations.Applied), len(migrations.Pending), len(migrations.Unknown))
	fmt.Println("result: ok (backup opened and unchanged)")
	return nil
}

func inspectBackupReadOnly(ctx context.Context, path string) (store.MigrationStatus, error) {
	st, err := store.OpenImmutableReadOnly(path)
	if err != nil {
		return store.MigrationStatus{}, fmt.Errorf("open backup read-only: %w", err)
	}
	defer st.Close()

	integrity, err := st.CheckIntegrity(ctx)
	if err != nil {
		return store.MigrationStatus{}, err
	}
	if integrity != "ok" {
		return store.MigrationStatus{}, fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	return st.MigrationStatus(ctx)
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
