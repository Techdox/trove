-- Track report liveness per host. An agent can report several hosts, so its
-- heartbeat cannot prove that every previously seen host is still present.
--
-- Existing hosts are seeded from their owning agent's last heartbeat. This
-- gives upgraded installations one grace window before host-level staleness
-- takes over; subsequent reports update only the host they describe.

ALTER TABLE hosts ADD COLUMN last_seen_at INTEGER;
ALTER TABLE hosts ADD COLUMN last_status TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN host_id INTEGER; -- soft reference; events outlive hosts

UPDATE hosts
   SET last_seen_at = (
       SELECT agents.last_seen_at
         FROM agents
        WHERE agents.id = hosts.agent_id
   );

-- Seed transition tracking from the owning agent. The server only emits host
-- liveness events while that agent is healthy, avoiding one alert per host
-- during a whole-agent outage while still detecting partial host loss.
UPDATE hosts
   SET last_status = COALESCE((
       SELECT agents.last_status
         FROM agents
        WHERE agents.id = hosts.agent_id
   ), '');

CREATE INDEX idx_hosts_last_seen ON hosts(last_seen_at);
CREATE INDEX idx_events_host ON events(host_id, at);
