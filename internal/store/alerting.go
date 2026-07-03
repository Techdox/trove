package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetMeta reads a bookkeeping value (e.g. the alert cursor). Returns ok=false
// when the key has never been set.
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get meta %q: %w", key, err)
	}
	return v, true, nil
}

// SetMeta upserts a bookkeeping value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("set meta %q: %w", key, err)
	}
	return nil
}

// MaxEventID returns the current highest event id (0 when empty). Used to
// seed the alert cursor so a fresh engine never replays history.
func (s *Store) MaxEventID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("max event id: %w", err)
	}
	return id.Int64, nil
}

// AlertState is the engine's per-key memory: the last value it alerted on and
// when it last sent for that key (cooldown).
type AlertState struct {
	Value  string
	SentAt int64
}

// GetAlertState reads engine state for a key; ok=false when unseen.
func (s *Store) GetAlertState(ctx context.Context, key string) (AlertState, bool, error) {
	var st AlertState
	err := s.db.QueryRowContext(ctx,
		`SELECT last_value, last_sent_at FROM alert_state WHERE key = ?`, key).
		Scan(&st.Value, &st.SentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, fmt.Errorf("get alert state %q: %w", key, err)
	}
	return st, true, nil
}

// SetAlertState upserts engine state for a key.
func (s *Store) SetAlertState(ctx context.Context, key string, st AlertState) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_state(key, last_value, last_sent_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET last_value = excluded.last_value, last_sent_at = excluded.last_sent_at`,
		key, st.Value, st.SentAt); err != nil {
		return fmt.Errorf("set alert state %q: %w", key, err)
	}
	return nil
}
