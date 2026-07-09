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

// AlertState is the engine's per-key memory:
//   - Value is the last-observed raw bad value ("" means good/no incident).
//   - Notified is whether a real notification was actually sent for the
//     CURRENT unbroken bad streak. It is deliberately a separate field from
//     Value so that a write which only updates the observed value (e.g. a
//     freshness check bouncing through "unknown") can never accidentally
//     erase the memory that an incident was already announced.
//   - SentAt anchors the cooldown window (the last time deliver() sent OR
//     suppressed-and-recorded something for this key).
type AlertState struct {
	Value    string
	Notified bool
	SentAt   int64
}

// GetAlertState reads engine state for a key; ok=false when unseen.
func (s *Store) GetAlertState(ctx context.Context, key string) (AlertState, bool, error) {
	var st AlertState
	var notified int
	err := s.db.QueryRowContext(ctx,
		`SELECT last_value, notified, last_sent_at FROM alert_state WHERE key = ?`, key).
		Scan(&st.Value, &notified, &st.SentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, fmt.Errorf("get alert state %q: %w", key, err)
	}
	st.Notified = notified != 0
	return st, true, nil
}

// SetAlertState upserts engine state for a key.
func (s *Store) SetAlertState(ctx context.Context, key string, st AlertState) error {
	notified := 0
	if st.Notified {
		notified = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_state(key, last_value, notified, last_sent_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET last_value = excluded.last_value, notified = excluded.notified, last_sent_at = excluded.last_sent_at`,
		key, st.Value, notified, st.SentAt); err != nil {
		return fmt.Errorf("set alert state %q: %w", key, err)
	}
	return nil
}

// ChannelDelivered reports whether channel already accepted this in-progress
// delivery. It lets the alert engine retry only channels that failed, rather
// than duplicating a notification to channels that already succeeded.
func (s *Store) ChannelDelivered(ctx context.Context, deliveryKey, channel string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM alert_channel_deliveries WHERE delivery_key = ? AND channel = ?`,
		deliveryKey, channel,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("check alert channel delivery %q/%q: %w", deliveryKey, channel, err)
	}
	return n > 0, nil
}

// MarkChannelDelivered records one successful channel delivery. The caller
// clears this short-lived state after all configured channels have succeeded.
func (s *Store) MarkChannelDelivered(ctx context.Context, deliveryKey, channel string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_channel_deliveries(delivery_key, channel, delivered_at) VALUES (?, ?, ?)
		 ON CONFLICT(delivery_key, channel) DO NOTHING`,
		deliveryKey, channel, s.now().Unix(),
	); err != nil {
		return fmt.Errorf("mark alert channel delivery %q/%q: %w", deliveryKey, channel, err)
	}
	return nil
}

// ClearChannelDeliveries removes retry bookkeeping after the entire fan-out
// completed. A subsequent, distinct incident starts with a clean slate.
func (s *Store) ClearChannelDeliveries(ctx context.Context, deliveryKey string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM alert_channel_deliveries WHERE delivery_key = ?`, deliveryKey); err != nil {
		return fmt.Errorf("clear alert channel deliveries %q: %w", deliveryKey, err)
	}
	return nil
}
