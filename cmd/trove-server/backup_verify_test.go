package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techdox/trove/internal/store"
)

func TestRunBackupVerifyReadsBackupWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trove-backup.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("create backup database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close backup database: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup before verification: %v", err)
	}

	output, err := captureDoctorOutput(t, func() error {
		return runBackup([]string{"verify", path})
	})
	if err != nil {
		t.Fatalf("verify backup: %v", err)
	}
	for _, want := range []string{
		"Trove backup verify",
		"database: ok (read-only integrity check)",
		"migrations:",
		"result: ok (backup opened and unchanged)",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("verification output missing %q:\n%s", want, output)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup after verification: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("backup changed during verification")
	}
}

func TestRunBackupVerifyRejectsNonRegularPath(t *testing.T) {
	err := runBackupVerify([]string{t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("verify directory error = %v, want regular-file error", err)
	}
}

func TestRunBackupVerifyRequiresPath(t *testing.T) {
	err := runBackupVerify(nil)
	if err == nil || !strings.Contains(err.Error(), "usage: trove-server backup verify <path>") {
		t.Fatalf("verify without path error = %v, want usage error", err)
	}
}

func TestRunBackupVerifyDoesNotCreateWALSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trove-backup.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("create backup database: %v", err)
	}
	if _, err := st.DB().Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close backup database: %v", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", sidecar, err)
		}
	}

	if _, err := captureDoctorOutput(t, func() error {
		return runBackupVerify([]string{path})
	}); err != nil {
		t.Fatalf("verify WAL-mode backup: %v", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(sidecar); !os.IsNotExist(err) {
			t.Errorf("verification created sidecar %s", sidecar)
		}
	}
}
