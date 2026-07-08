package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupWritesDatabaseAndRefusesOverwrite(t *testing.T) {
	st, _ := newTestStore(t)
	dst := filepath.Join(t.TempDir(), "trove-backup.db")
	if err := st.Backup(context.Background(), dst); err != nil {
		t.Fatalf("backup: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("backup is empty")
	}
	if err := st.Backup(context.Background(), dst); err == nil {
		t.Fatal("second backup unexpectedly overwrote existing file")
	}
}
