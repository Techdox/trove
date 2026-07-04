-- Phase 4 fix: track "was an alert actually sent for the current bad streak"
-- as its own column, independent of the last-observed value.
--
-- Previously this was encoded by prefixing last_value with "~" for
-- known-bad-but-unsent state. That conflated two different facts — "what is
-- the current value" and "did we tell anyone about it" — into one string, so
-- any write that only updates the value (e.g. a freshness check bouncing
-- through "unknown" between "outdated" and "current") destroyed the
-- notified bit and silently swallowed the eventual resolved notice.

ALTER TABLE alert_state ADD COLUMN notified INTEGER NOT NULL DEFAULT 0;

-- Best-effort backfill so any state accumulated under the old encoding
-- reads sensibly under the new one: a "~"-prefixed value was known-bad but
-- unsent (strip the marker, notified=0); any other non-empty value was
-- sent under the old scheme (notified=1); empty stays notified=0.
UPDATE alert_state SET notified = 1 WHERE last_value != '' AND substr(last_value, 1, 1) != '~';
UPDATE alert_state SET last_value = substr(last_value, 2) WHERE substr(last_value, 1, 1) = '~';
