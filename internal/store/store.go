// Package store is the SQLite persistence layer for the Trove server. It owns
// the schema, migrations, the report-ingest transaction, and the read queries
// that back the dashboard APIs. Nothing outside this package touches SQL.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the database handle. It is safe for concurrent use.
type Store struct {
	db *sql.DB
	// now returns the current time; overridable in tests. Always used as UTC.
	now func() time.Time
}

// Open opens (creating if needed) the SQLite database at path and applies any
// pending migrations. Pass ":memory:" for an ephemeral store (tests).
func Open(path string) (*Store, error) {
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is a single-writer engine. Serializing connections keeps the
	// ingest transaction, the staleness ticker, and dashboard reads from
	// tripping over SQLITE_BUSY at Phase 1 volumes; busy_timeout is a belt to
	// the suspenders.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func buildDSN(path string) string {
	// modernc accepts PRAGMAs via the DSN query string.
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if path != ":memory:" {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	return "file:" + path + "?" + q.Encode()
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for health checks (Ping). Callers must not run
// schema-mutating statements through it.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // lexical order == apply order (0001_, 0002_, ...)

	for _, name := range names {
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			name, time.Now().UTC().Unix(),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
