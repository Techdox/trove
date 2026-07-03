package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrAgentExists is returned by CreateAgent when the name is already taken.
var ErrAgentExists = errors.New("agent with that name already exists")

// ErrAgentNotFound is returned when a token matches no agent.
var ErrAgentNotFound = errors.New("agent not found")

const tokenPrefix = "trove_"

// Agent is a stored agent row (without the token hash).
type Agent struct {
	ID              int64
	Name            string
	Platform        string
	Version         string
	IntervalSeconds int
	CreatedAt       int64
	LastSeenAt      sql.NullInt64
	// LastStatus is the most recent heartbeat verdict recorded by the
	// staleness loop ("ok"/"stale"/"offline"; empty until first evaluated).
	LastStatus string
}

// HashToken returns the hex SHA-256 of a bearer token. Tokens are
// high-entropy random strings, so a plain cryptographic hash (not a slow
// password hash) is the right and intended choice.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// generateToken returns a new opaque bearer token with the trove_ prefix.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// CreateAgent creates a new agent and returns the one-time plaintext token.
// The plaintext is never stored; only its SHA-256 is persisted. Show it to the
// operator once.
func (s *Store) CreateAgent(ctx context.Context, name string) (token string, agent Agent, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Agent{}, errors.New("agent name is required")
	}
	token, err = generateToken()
	if err != nil {
		return "", Agent{}, err
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO agents(name, token_hash, created_at) VALUES (?, ?, ?)`,
		name, HashToken(token), now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return "", Agent{}, ErrAgentExists
		}
		return "", Agent{}, fmt.Errorf("insert agent: %w", err)
	}
	id, _ := res.LastInsertId()
	return token, Agent{ID: id, Name: name, CreatedAt: now}, nil
}

// EnsureAgentWithToken creates an agent with a caller-supplied token when no
// agent of that name exists yet, and reports whether it created one. This
// backs the docker-compose dev bootstrap (TROVE_BOOTSTRAP_*); production mints
// random tokens via CreateAgent. If the name already exists its token is left
// untouched (idempotent across restarts).
func (s *Store) EnsureAgentWithToken(ctx context.Context, name, token string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.TrimSpace(token) == "" {
		return false, errors.New("bootstrap agent name and token are both required")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO agents(name, token_hash, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(name) DO NOTHING`,
		name, HashToken(token), s.now().Unix(),
	)
	if err != nil {
		return false, fmt.Errorf("ensure agent: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AuthenticateByToken resolves a bearer token to an agent. It hashes the
// presented token and looks up the matching row. Returns ErrAgentNotFound if
// there is no match.
func (s *Store) AuthenticateByToken(ctx context.Context, token string) (Agent, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Agent{}, ErrAgentNotFound
	}
	hash := HashToken(token)
	var a Agent
	var storedHash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, platform, version, report_interval_seconds, created_at, last_seen_at, token_hash
		   FROM agents WHERE token_hash = ?`, hash,
	).Scan(&a.ID, &a.Name, &a.Platform, &a.Version, &a.IntervalSeconds, &a.CreatedAt, &a.LastSeenAt, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrAgentNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("authenticate: %w", err)
	}
	// Constant-time confirm — the indexed lookup already matched, but this
	// keeps the comparison uniform and defends against any future non-unique
	// lookup path.
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hash)) != 1 {
		return Agent{}, ErrAgentNotFound
	}
	return a, nil
}

// ListAgents returns all agents ordered by name, for the /agents API.
func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, platform, version, report_interval_seconds, created_at, last_seen_at, last_status
		   FROM agents ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Version, &a.IntervalSeconds, &a.CreatedAt, &a.LastSeenAt, &a.LastStatus); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAgentStatus records an agent's heartbeat verdict (ok/stale/offline),
// emitting a kind='agent' event when it changes. The first-ever evaluation
// (empty last_status) seeds silently so a fresh install or a migrated database
// doesn't fire a wave of spurious "agent is ok" events. Returns whether a
// transition event was recorded.
func (s *Store) UpdateAgentStatus(ctx context.Context, agentID int64, agentName, status string) (bool, error) {
	now := s.now().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	var last string
	if err := tx.QueryRowContext(ctx,
		`SELECT last_status FROM agents WHERE id = ?`, agentID).Scan(&last); err != nil {
		return false, fmt.Errorf("read agent status: %w", err)
	}
	if last == status {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET last_status = ? WHERE id = ?`, status, agentID); err != nil {
		return false, fmt.Errorf("update agent status: %w", err)
	}
	if last == "" {
		return false, tx.Commit() // silent seed
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events(kind, agent_id, agent, from_state, to_state, at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		EventKindAgent, agentID, agentName, last, status, now,
	); err != nil {
		return false, fmt.Errorf("insert agent event: %w", err)
	}
	return true, tx.Commit()
}

// DeleteAgent removes an agent and (via cascade) its hosts, services, and
// events. Returns ErrAgentNotFound if no such agent.
func (s *Store) DeleteAgent(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE name = ?`, strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
// modernc surfaces these as messages containing "UNIQUE constraint failed".
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
