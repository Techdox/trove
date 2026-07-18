package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/pkg/model"
)

func TestRunSupervisedRestartsPanickingWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := newWorkerMonitor("test")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var attempts atomic.Int32
	running := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSupervisedWithBackoff(ctx, logger, "test", monitor, time.Millisecond, 2*time.Millisecond, func() {
			if attempts.Add(1) < 3 {
				panic("test panic")
			}
			close(running)
			<-ctx.Done()
		})
	}()

	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("worker was not restarted after panic")
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("worker attempts = %d, want 3", got)
	}
	if err := monitor.health(); err != nil {
		t.Fatalf("worker health after restart = %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after context cancellation")
	}
}

func TestRunSupervisedMarksWorkerUnavailableDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	monitor := newWorkerMonitor("freshness")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		runSupervisedWithBackoff(ctx, logger, "freshness", monitor, time.Hour, time.Hour, func() {
			close(started)
			panic("test panic")
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	deadline := time.Now().Add(time.Second)
	for {
		err := monitor.health()
		if err != nil {
			if !strings.Contains(err.Error(), "freshness") {
				t.Fatalf("worker health error = %q, want worker name", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker was not marked unavailable during backoff")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not leave backoff after context cancellation")
	}
}

type compatibilitySnapshot struct {
	agents         int
	hosts          int
	services       int
	events         int
	imageChecks    int
	meta           int
	alertState     int
	serviceParents int
}

func TestBackupUpgradeRestoreRollbackCompatibility(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.db")
	source, err := store.Open(sourcePath)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	_, agent, err := source.CreateAgent(ctx, "compat-agent")
	if err != nil {
		t.Fatalf("create representative agent: %v", err)
	}
	report := &model.Report{
		Agent: model.ReportAgent{Name: "compat-agent", Platform: model.PlatformDocker, Version: "0.15.1"},
		Host:  model.ReportHost{Hostname: "compat-host", Meta: map[string]string{"fixture": "v0.15.1"}},
		Services: []model.ReportService{
			{ExternalID: "parent", Name: "compat-parent", Kind: model.KindContainer, Image: "example.invalid/parent:1", ImageDigest: "sha256:parent", State: "running", Health: model.HealthHealthy},
			{ExternalID: "child", ParentExternalID: "parent", Name: "compat-child", Kind: model.KindContainer, Image: "example.invalid/child:1", ImageDigest: "sha256:child", State: "running", Health: model.HealthHealthy},
		},
	}
	if err := source.ApplyReport(ctx, agent.ID, report); err != nil {
		t.Fatalf("apply representative report: %v", err)
	}
	if _, err := source.DB().ExecContext(ctx, `
		INSERT INTO image_checks(image, latest_digest, status, checked_at, next_check_at)
		VALUES ('example.invalid/parent:1', 'sha256:latest', 'ok', 1, 2);
		INSERT INTO meta(key, value) VALUES ('alert_cursor', '1');
		INSERT INTO alert_state(key, last_value, last_sent_at, notified)
		VALUES ('svc:1:health', 'healthy', 1, 1);
	`); err != nil {
		t.Fatalf("seed representative auxiliary records: %v", err)
	}
	want := snapshotStore(t, source)

	backupPath := filepath.Join(root, "immutable", "v0.15.1.db")
	t.Setenv("TROVE_DB", sourcePath)
	if err := runBackup([]string{backupPath}); err != nil {
		t.Fatalf("hot backup: %v", err)
	}
	if err := runBackup([]string{backupPath}); err == nil {
		t.Fatal("backup unexpectedly overwrote an existing rollback point")
	}
	assertMode(t, backupPath, 0o600)
	assertMode(t, filepath.Dir(backupPath), 0o700)
	assertStoreSnapshot(t, backupPath, want)

	upgradePath := copyDatabase(t, backupPath, filepath.Join(root, "upgrade.db"))
	assertStoreSnapshot(t, upgradePath, want)
	assertStoreSnapshot(t, upgradePath, want) // migrations and restart are idempotent

	restorePath := copyDatabase(t, backupPath, filepath.Join(root, "restore.db"))
	assertStoreSnapshot(t, restorePath, want)

	rollbackPath := copyDatabase(t, backupPath, filepath.Join(root, "rollback.db"))
	assertStoreSnapshot(t, rollbackPath, want)
}

func snapshotStore(t *testing.T, st *store.Store) compatibilitySnapshot {
	t.Helper()
	var integrity string
	if err := st.DB().QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity check = %q, want ok", integrity)
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := st.DB().QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return n
	}
	return compatibilitySnapshot{
		agents:         count("SELECT COUNT(*) FROM agents"),
		hosts:          count("SELECT COUNT(*) FROM hosts"),
		services:       count("SELECT COUNT(*) FROM services"),
		events:         count("SELECT COUNT(*) FROM events"),
		imageChecks:    count("SELECT COUNT(*) FROM image_checks"),
		meta:           count("SELECT COUNT(*) FROM meta"),
		alertState:     count("SELECT COUNT(*) FROM alert_state"),
		serviceParents: count("SELECT COUNT(*) FROM services WHERE parent_id IS NOT NULL"),
	}
}

func assertStoreSnapshot(t *testing.T, path string, want compatibilitySnapshot) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", filepath.Base(path), err)
	}
	got := snapshotStore(t, st)
	if err := st.Close(); err != nil {
		t.Fatalf("close %s: %v", filepath.Base(path), err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot for %s = %+v, want %+v", filepath.Base(path), got, want)
	}
}

func copyDatabase(t *testing.T, src, dst string) string {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create database copy: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy database: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close database copy: %v", err)
	}
	return dst
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}
