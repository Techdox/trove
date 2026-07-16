-- Persist the latest platform-reported host condition and resource snapshot.
-- Heartbeat status remains server-derived from last_seen_at; condition is a
-- separate platform verdict and metrics are informational point-in-time data.

ALTER TABLE hosts ADD COLUMN condition TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE hosts ADD COLUMN metrics_json TEXT NOT NULL DEFAULT '{}';
