package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestHostLivenessMigrationBackfillsExistingHosts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open pre-migration database: %v", err)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for _, name := range []string{
		"0001_init.sql",
		"0002_image_checks.sql",
		"0003_service_parent.sql",
		"0004_event_model.sql",
		"0005_alert_notified.sql",
		"0006_health_detail.sql",
		"0007_alert_channel_deliveries.sql",
	} {
		script, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)`, name); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	const lastSeen = int64(1_767_225_600)
	res, err := db.ExecContext(ctx, `
		INSERT INTO agents(name, token_hash, created_at, last_seen_at, last_status)
		VALUES ('proxmox-a', 'hash', 0, ?, 'ok')`, lastSeen)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	agentID, _ := res.LastInsertId()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO hosts(agent_id, hostname) VALUES (?, 'node-a')`, agentID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-migration database: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded database: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var got sql.NullInt64
	var status string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT last_seen_at, last_status FROM hosts WHERE hostname = 'node-a'`).Scan(&got, &status); err != nil {
		t.Fatalf("read migrated host heartbeat: %v", err)
	}
	if !got.Valid || got.Int64 != lastSeen {
		t.Fatalf("migrated last_seen_at = %+v, want %d", got, lastSeen)
	}
	if status != "ok" {
		t.Fatalf("migrated last_status = %q, want ok", status)
	}
}
