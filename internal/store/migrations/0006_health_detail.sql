-- Add a short, human-readable reason for a service's health — e.g. a failing
-- Docker healthcheck's last output, or a Kubernetes pod's CrashLoopBackOff /
-- termination reason. Purely supplementary display data: it is never on the
-- state/health diff or event path, so ingest just overwrites it each report.
ALTER TABLE services ADD COLUMN health_detail TEXT NOT NULL DEFAULT '';
