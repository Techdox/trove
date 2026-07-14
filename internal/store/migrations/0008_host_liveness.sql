-- Track report liveness per host. An agent can report several hosts, so its
-- heartbeat cannot prove that every previously seen host is still present.
--
-- Existing hosts are seeded from their owning agent's last heartbeat. This
-- gives upgraded installations one grace window before host-level staleness
-- takes over; subsequent reports update only the host they describe.

ALTER TABLE hosts ADD COLUMN last_seen_at INTEGER;

UPDATE hosts
   SET last_seen_at = (
       SELECT agents.last_seen_at
         FROM agents
        WHERE agents.id = hosts.agent_id
   );

CREATE INDEX idx_hosts_last_seen ON hosts(last_seen_at);
