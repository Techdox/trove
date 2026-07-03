-- Phase 3: parent/child relation between services.
--
-- A child instance (e.g. a Kubernetes pod) points at its parent workload (e.g.
-- a Deployment) via parent_id. Standalone services (containers, VMs, LXCs,
-- processes) leave it NULL. Deliberately a plain nullable column, not a foreign
-- key: SQLite ALTER TABLE ADD COLUMN can't add every FK form cleanly across
-- versions, and referential integrity here is simple enough to manage in the
-- ingest/prune code (which nulls dangling references before pruning parents).

ALTER TABLE services ADD COLUMN parent_id INTEGER;

CREATE INDEX idx_services_parent ON services(parent_id);
