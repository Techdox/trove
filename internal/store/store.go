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

// OpenReadOnly opens an existing database without creating it or applying
// migrations. It is for diagnostics and backup verification commands that
// must not change the database they inspect.
func OpenReadOnly(path string) (*Store, error) {
	return openReadOnly(path, false)
}

// OpenImmutableReadOnly opens a complete SQLite backup without creating or
// consulting WAL sidecar files. It is for standalone backup verification only;
// use OpenReadOnly for diagnostics against a live database, which may have an
// active WAL.
func OpenImmutableReadOnly(path string) (*Store, error) {
	return openReadOnly(path, true)
}

func openReadOnly(path string, immutable bool) (*Store, error) {
	db, err := sql.Open("sqlite", buildReadOnlyDSN(path, immutable))
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

func buildDSN(path string) string {
	// modernc accepts PRAGMAs via the DSN query string.
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if path != ":memory:" {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	return buildFileDSN(path, q)
}

func buildReadOnlyDSN(path string, immutable bool) string {
	q := url.Values{}
	q.Set("mode", "ro")
	if immutable {
		q.Set("immutable", "1")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	// query_only is connection-local. It is redundant with mode=ro, but keeps
	// the diagnostic connection unable to write if the driver ever changes how
	// it handles read-only URI mode.
	q.Add("_pragma", "query_only(1)")
	return buildFileDSN(path, q)
}

func buildFileDSN(path string, q url.Values) string {
	// Constructing a SQLite URI by string concatenation would let a legal
	// filename containing ? or # change the URI query or fragment. URL escapes
	// the filesystem path independently from SQLite's connection parameters.
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for health checks (Ping). Callers must not run
// schema-mutating statements through it.
func (s *Store) DB() *sql.DB { return s.db }

// CheckIntegrity runs SQLite's integrity check without changing the database.
// A healthy database returns "ok".
func (s *Store) CheckIntegrity(ctx context.Context) (string, error) {
	var result string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return "", fmt.Errorf("integrity check: %w", err)
	}
	return result, nil
}

// MigrationStatus describes the migrations recorded by a database relative to
// the migrations embedded in this binary.
type MigrationStatus struct {
	Applied []string
	Pending []string
	Retired []string
	Unknown []string
}

// retiredMigrations are historical migration records that may remain in
// databases created by pre-release builds. Keep these exact names recognized
// without applying them to new databases or weakening detection of migrations
// created by a genuinely newer binary.
var retiredMigrations = map[string]struct{}{
	"0008_runtime_settings.sql": {},
}

// MigrationStatus reads migration state without applying any migrations.
func (s *Store) MigrationStatus(ctx context.Context) (MigrationStatus, error) {
	expected, err := migrationNames()
	if err != nil {
		return MigrationStatus{}, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("read schema migrations: %w", err)
	}
	defer rows.Close()

	recorded := make(map[string]bool, len(expected))
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return MigrationStatus{}, fmt.Errorf("read schema migration: %w", err)
		}
		recorded[version] = true
	}
	if err := rows.Err(); err != nil {
		return MigrationStatus{}, fmt.Errorf("read schema migrations: %w", err)
	}

	status := MigrationStatus{}
	expectedSet := make(map[string]bool, len(expected))
	for _, name := range expected {
		expectedSet[name] = true
		if recorded[name] {
			status.Applied = append(status.Applied, name)
		} else {
			status.Pending = append(status.Pending, name)
		}
	}
	for name := range recorded {
		if _, retired := retiredMigrations[name]; retired {
			status.Retired = append(status.Retired, name)
		} else if !expectedSet[name] {
			status.Unknown = append(status.Unknown, name)
		}
	}
	sort.Strings(status.Retired)
	sort.Strings(status.Unknown)
	return status, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

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

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // lexical order == apply order (0001_, 0002_, ...)
	return names, nil
}
