-- Trove Phase 1 schema.
-- Timestamps are stored as INTEGER unix seconds (UTC). JSON blobs are stored
-- as TEXT. Internal ids are autoincrement integers; platform-native ids live
-- in *_external / external_id columns.

CREATE TABLE agents (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    name                    TEXT    NOT NULL UNIQUE,
    token_hash              TEXT    NOT NULL UNIQUE,   -- hex SHA-256 of bearer token
    platform                TEXT    NOT NULL DEFAULT '',
    version                 TEXT    NOT NULL DEFAULT '',
    report_interval_seconds INTEGER NOT NULL DEFAULT 0, -- 0 => use server default
    created_at              INTEGER NOT NULL,
    last_seen_at            INTEGER                     -- NULL until first report
);

CREATE TABLE hosts (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id           INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    hostname           TEXT    NOT NULL,
    platform_meta_json TEXT    NOT NULL DEFAULT '{}',
    UNIQUE (agent_id, hostname)
);

CREATE TABLE services (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    host_id       INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    external_id   TEXT    NOT NULL,
    name          TEXT    NOT NULL DEFAULT '',
    kind          TEXT    NOT NULL DEFAULT 'container',
    image         TEXT    NOT NULL DEFAULT '',
    image_digest  TEXT    NOT NULL DEFAULT '',
    state         TEXT    NOT NULL DEFAULT '',
    health        TEXT    NOT NULL DEFAULT 'unknown',
    ports_json    TEXT    NOT NULL DEFAULT '[]',
    labels_json   TEXT    NOT NULL DEFAULT '{}',
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,   -- last time actually present in a report
    updated_at    INTEGER NOT NULL,   -- last time any field changed
    UNIQUE (host_id, external_id)
);

CREATE INDEX idx_services_host ON services(host_id);
CREATE INDEX idx_services_updated ON services(updated_at);

CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    from_state TEXT    NOT NULL DEFAULT '',
    to_state   TEXT    NOT NULL DEFAULT '',
    at         INTEGER NOT NULL
);

CREATE INDEX idx_events_service ON events(service_id, at);
CREATE INDEX idx_events_at ON events(at);
