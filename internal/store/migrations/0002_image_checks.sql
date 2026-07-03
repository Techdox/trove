-- Phase 2: image-freshness cache.
--
-- One row per distinct image reference seen across services. Holds the latest
-- manifest digest resolved from the registry plus scheduling/backoff state.
-- This table is a cache only: it is never on the report-ingest write path, and
-- per-service freshness is derived at read time by comparing a service's
-- running image_digest to latest_digest here.

CREATE TABLE image_checks (
    image         TEXT    PRIMARY KEY,          -- full image ref, e.g. "gitea/gitea:1.22"
    latest_digest TEXT    NOT NULL DEFAULT '',  -- registry manifest digest (sha256:...), '' if unknown
    status        TEXT    NOT NULL DEFAULT 'unknown', -- ok | error | unknown
    error         TEXT    NOT NULL DEFAULT '',  -- last error message, if status=error
    checked_at    INTEGER,                      -- last time a check was attempted (NULL = never)
    next_check_at INTEGER NOT NULL DEFAULT 0    -- earliest time to check again (backoff-aware)
);

CREATE INDEX idx_image_checks_due ON image_checks(next_check_at);
