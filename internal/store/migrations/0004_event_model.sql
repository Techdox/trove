-- Phase 4: unified event model + alerting state.
--
-- The events table is rebuilt because its shape changes in three ways:
--   * kind column: 'state' (service state), 'health' (service health),
--     'agent' (heartbeat status transitions) — the dashboard feed and the
--     alert engine consume the same stream;
--   * service_id becomes nullable (agent events have no service) and loses
--     its FK: events are history and must OUTLIVE the rows they describe
--     (previously ON DELETE CASCADE silently erased a pruned service's past);
--   * display fields (service, hostname, agent) are denormalized onto the
--     event at write time, so an event stays renderable after its subject is
--     pruned and the feed needs no joins.

ALTER TABLE agents ADD COLUMN last_status TEXT NOT NULL DEFAULT '';

CREATE TABLE events_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL DEFAULT 'state',  -- state | health | agent
    service_id INTEGER,                           -- soft reference, no FK
    agent_id   INTEGER,                           -- soft reference, no FK
    service    TEXT    NOT NULL DEFAULT '',       -- denormalized display name
    hostname   TEXT    NOT NULL DEFAULT '',
    agent      TEXT    NOT NULL DEFAULT '',
    from_state TEXT    NOT NULL DEFAULT '',
    to_state   TEXT    NOT NULL DEFAULT '',
    at         INTEGER NOT NULL
);

INSERT INTO events_new (id, kind, service_id, service, hostname, agent, from_state, to_state, at)
SELECT e.id, 'state', e.service_id, s.name, h.hostname, a.name, e.from_state, e.to_state, e.at
  FROM events e
  JOIN services s ON s.id = e.service_id
  JOIN hosts h    ON h.id = s.host_id
  JOIN agents a   ON a.id = h.agent_id;

DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

CREATE INDEX idx_events_at ON events(at);
CREATE INDEX idx_events_kind_at ON events(kind, at);
CREATE INDEX idx_events_service ON events(service_id, at);

-- Small key/value store for engine bookkeeping (e.g. the alert cursor,
-- last digest timestamp).
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Alert engine state: cooldown timestamps, last-alerted values, and freshness
-- fingerprints, keyed like 'svc:<id>:health' / 'agent:<id>' / 'fresh:<id>'.
CREATE TABLE alert_state (
    key          TEXT    PRIMARY KEY,
    last_value   TEXT    NOT NULL DEFAULT '',
    last_sent_at INTEGER NOT NULL DEFAULT 0
);
