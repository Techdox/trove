# Trove Roadmap

**Status:** `v0.16.1` is the current public release. <!-- x-release-please-version --> The core product is
delivered: Docker/Kubernetes/Proxmox/bare-metal agents, per-agent token auth,
heartbeat/staleness, image freshness, parent/child workloads, host condition
and resources, configurable retention, alerting, OIDC, observability APIs, and
the read-only dashboard. See the [README](README.md) for the current feature
surface.

There is no remaining pre-`v1.0.0` headline feature work. The path to 1.0 is
about day-two operations, compatibility promises, scale evidence, and time in
real deployments. Helm packaging and certificate-expiry monitoring remain
post-1.0 work so they do not expand the support surface during stabilization.

## `v0.16` hardening ✅ delivered

`v0.16.0` and the `v0.16.1` release fix completed the reliability, security,
compatibility, UI, documentation, and release-confidence milestone:

- Authentik OIDC was validated across the deployments, including browser
  login/callback/logout/session flows, `TROVE_API_TOKEN`, agent ingest, and
  `/healthz`.
- Background workers gained bounded restart supervision and health/metrics
  visibility; HTTP responses gained security and cache controls; report ingest
  gained bounded-body and trailing-data rejection.
- Frozen previous-release reports and a real `v0.15.1` database proved the
  server-first upgrade, restart, backup, restore, and rollback paths. The
  representative lifecycle remains covered in the test suite.
- Kubernetes collection and registry authentication, redirect, SSRF, and
  digest-fetch behavior gained integration coverage.
- Mobile service status, dashboard accessibility, troubleshooting guidance,
  and automated Markdown link checking were completed.
- Release archives and multi-platform images now ship with checksums, SBOMs,
  provenance, digest records, and keyless GitHub attestations.
- `main` requires pull requests, up-to-date `test` and `security` checks, and
  resolved review conversations. A separate approving reviewer is not required
  for the current solo-maintainer workflow.

## Current milestone — `v0.17.0` operator confidence

**Goal:** make routine ownership boring before declaring the interfaces stable.
This milestone does not add another monitored platform or any control path.

### Agent credential rotation

- [ ] Add `trove-server agent rotate <name>` to issue a one-time replacement
  token without deleting the agent, its hosts, services, or history.
- [ ] Invalidate the previous token when rotation succeeds, never persist the
  replacement in plaintext, and cover the CLI/store behavior with tests.
- [ ] Document a maintenance-window rotation flow for Compose, Kubernetes,
  Proxmox, and systemd agents, including the expected temporary `401` state.

### Diagnostics and recovery

- [ ] Add a sanitized `trove-server doctor` command covering database access
  and integrity, migration state, configuration validation, and non-secret
  operational facts suitable for a bug report.
- [ ] Add a supported backup-verification command or workflow that checks
  SQLite integrity and proves the backup can be opened without modifying it.
- [ ] Publish cron and systemd-timer backup examples with retention guidance,
  plus a short restore-rehearsal checklist.

### UI and documentation regression protection

- [ ] Add automated desktop/tablet/mobile browser coverage for the dashboard's
  highest-risk layouts: long Kubernetes names and kind labels, event dates and
  status colors, drawers, filters, and the no-horizontal-scroll mobile view.
- [ ] Keep the browser suite test-only; the shipped dashboard remains vanilla
  JavaScript/CSS with no frontend build or runtime dependency.
- [ ] Make repository docs the source of truth, define what belongs in the
  wiki, and prevent duplicated upgrade/authentication guidance from drifting.

**Exit:** complete the gates above, exercise the resulting commands against a
copy of real deployment data, and run the candidate on the main deployment
without changing Trove's observation-only boundary.

## Following milestone — `v0.18.0` stability contract

**Goal:** define what 1.0 promises and prove Trove operates comfortably beyond
the current deployment.

- [ ] Publish machine-readable schemas for the agent report and `/api/v1`
  responses, with compatibility tests for additive evolution.
- [ ] Turn the server-first, immediately-previous-agent guarantee into a clear
  version support matrix and test it on every release candidate.
- [ ] Add a reproducible scale scenario covering at least 50 agents, 10,000
  services, and 100,000 retained events. Record ingest latency, API response
  size/time, database growth, maintenance duration, and restart time.
- [ ] Protect the current coverage baseline and raise useful coverage in the
  agent framework, server CLI, and store failure paths; do not chase a global
  percentage with low-value tests.
- [ ] Validate fresh installs and upgrades with several independent operators
  across Docker, Kubernetes, Proxmox, and bare-metal Linux.

## `v1.0.0` release gates

- [ ] Freeze headline features during the release-candidate period; accept only
  documentation, compatibility, security, and regression fixes.
- [ ] Complete a 4–6 week candidate soak with no unresolved serious regression,
  worker instability, data-integrity failure, or alert-delivery failure.
- [ ] Rehearse supported upgrades, backup/restore, and rollback with production-
  representative data and published artifacts.
- [ ] Finalize API/report compatibility, upgrade, security, release-integrity,
  and maintainer-support statements.
- [ ] Confirm every install path and example pins or discovers the intended
  release, and that release assets/images/attestations are complete.

## After `v1.0.0`

Prioritize these only after the stability contract is proven:

- Helm chart for the server and Kubernetes agent.
- Certificate-expiry monitoring with explicit target, SNI, timeout, and
  self-signed-certificate policies.
- Named, independently rotatable read-only API tokens.
- Saved/shareable dashboard filters and read-only deep links to the systems
  that own workloads.
- Additional agents only when real operator demand justifies their long-term
  support cost.

## Principles carried forward

Every phase must preserve the Phase 1 invariants:

- **Read-only, always.** No phase adds a deploy/restart/exec/edit path.
- **Everything normalizes into `services`.** New platforms map their world onto
  the existing model rather than growing parallel tables.
- **Push model, full-state reports, idempotent ingest.** New agents reuse the
  `pkg/model` contract and the ingest transaction.
- **Single static binary, pure Go.** No CGO, no per-platform build complexity.

---

## Phase 2 — Image freshness ✅ delivered

**Goal:** show whether each running image is current — "up to date", "N versions
behind", or "update available".

**Groundwork already in place:** the Docker agent already captures the registry
manifest digest into `services.image_digest`, which is exactly what a freshness
check compares against. Nothing about the running-state path changes.

**Design:**
- Resolve the *latest* digest for each image's tag from its registry, and
  compare to the captured `image_digest`.
- **The trap the spec called out:** doing this per-container hits Docker Hub's
  anonymous rate limits almost immediately. Freshness must be a **server-side,
  per-registry batched job with a cached result + TTL**, decoupled from ingest —
  not a lookup per container per report.
- Handle per-registry auth (Docker Hub token, GHCR, private registries),
  exponential backoff on 429s, and graceful "unknown" when a registry is
  unreachable.

**New schema (additive):** an `image_checks` cache table keyed by
`(registry, repository, tag)` holding `latest_digest`, `checked_at`,
`next_check_at`. Services join to it for display; it is never on the write path.

**New surface:** a freshness field per service in `/api/v1/services` and a badge
in the dashboard. Config for check cadence and registry credentials.

**Non-goals:** auto-update anything (that would violate read-only), semver
reasoning beyond digest equality.

---

## Phase 3 — Additional agents (K8s, Proxmox, bare metal) ✅ delivered

**Goal:** the same catalog across Kubernetes, Proxmox, and plain hosts. The data
model was normalized for exactly this — `kind` already reserves `pod`, `vm`,
`lxc`, `process`.

Kubernetes forces the one real schema change coming, so its design is pinned in
**[Decision D2](#d2--parentchild-schema)** below. The rest reuse everything:
push model, auth, staleness, dashboard. An agent's whole job is to map its
platform onto `model.Report`.

- **Kubernetes:** Deployments/StatefulSets/DaemonSets → parent services; Pods →
  child instances. Health rolls up from children.
- **Proxmox:** VMs and LXCs (`kind=vm|lxc`), one agent per node or per cluster.
- **Bare metal / local:** long-running processes (`kind=process`).

---

## Phase 4 — Notifications & reporting ✅ delivered

**Goal:** stop needing to watch the dashboard.

**Delivered** (see [docs/alerts.md](docs/alerts.md)):

- Unified event stream: state, health, and agent-heartbeat transitions all
  land in `events` (denormalized so history outlives its subjects), shown in
  the dashboard feed and consumed by the alert engine via a persistent cursor.
- Instant channels: generic webhook, Discord, ntfy — with severity levels,
  recovery notices, per-key cooldown/flap suppression, escalation bypass, and
  silent seeding (no alarm floods at boot).
- Scheduled email digest (SMTP, `daily@HH:MM` / `weekly@day:HH:MM`).
- `trove-server alert test` to verify channel config.
- **Decision D1 resolved**: retention configurable (`TROVE_EVENT_RETENTION`
  default 30d, `TROVE_REMOVED_RETENTION` default 24h), pruning moved off the
  ingest write path onto an hourly maintenance loop.

**Deferred out of this phase:** cert-expiry monitoring — needs its own probing
design (targets, SNI, self-signed policy); slated after `v1.0.0`.

---

## Phase 5 — Auth & packaging

**Goal:** make Trove safe to expose beyond a trusted network, and easy to deploy.

**Delivered in `v0.10.0`:**

- **OIDC** on the dashboard and read APIs — **delivered.** Any standard OIDC
  provider (Authentik, Keycloak, Auth0, Google, Dex) is supported via
  `TROVE_OIDC_*` env vars. When configured, the dashboard redirects
  unauthenticated browser requests to the IdP; API clients can use
  `TROVE_API_TOKEN` for Bearer-token access. Agent ingest and `/healthz`
  are never gated. When OIDC is not configured, the dashboard is open
  (backward compatible with Phase 1).

**Validated for `v1.0.0`:**

- **OIDC and documentation confidence pass — complete.** Authentik is in use on
  the main Trove deployment and has been tested across the deployments. Browser
  authentication and session flows, API-token access, agent ingest, `/healthz`,
  and the supporting docs/wiki/example/upgrade guidance have been verified.

**Remaining after `v1.0.0`:**

- **Helm chart** for Kubernetes deployment (natural companion to the Phase 3 K8s
  agent), planned after `v1.0.0`.
- **Certificate-expiry monitoring** for HTTPS targets, including target config,
  SNI handling, and self-signed certificate policy, planned after `v1.0.0`.

---

## Decisions to make before they bite

These two ripple across later phases. Deciding them once, early, is far cheaper
than migrating twice.

### D1 — Retention & history ✅ resolved (Phase 4)

**Problem:** Phase 1 keeps only the latest state per service plus a 24h rolling
event log, pruned on the write path. That is deliberately minimal and is fine
for a live dashboard, but Phase 4 breaks against it: alerting can't miss events,
and weekly digests need more than 24h of history.

**Implemented as proposed** (migration 0004 + `store.Prune` + the server's
hourly maintenance loop):
- Make retention **configurable** (`TROVE_EVENT_RETENTION` default 30d,
  `TROVE_REMOVED_RETENTION` default 24h).
- **Move pruning off the write path** to a periodic maintenance job once volume
  grows — pruning inside every ingest transaction won't scale to many agents.
- Keep `events` as the append-only source of truth; add downsampling/rollups
  only if raw volume becomes a problem.
- Each new cache (the Phase 2 `image_checks` table) gets its *own* retention;
  don't couple them.

### D2 — Parent/child schema ✅ resolved (Phase 3)

**Problem:** in Phase 1 a Docker container is simultaneously the logical unit and
its only instance, so `services` is flat. Kubernetes breaks that: one Deployment
has N Pod instances. The spec anticipated this — "the `services` table may grow a
parent/child relation."

**Proposed direction (self-referential, additive, backward-compatible):**

```sql
ALTER TABLE services ADD COLUMN parent_id INTEGER NULL REFERENCES services(id);
-- container / vm / lxc / process: parent_id stays NULL (unit == instance).
-- pod: parent_id -> the Deployment's service row (kind='deployment' etc.).
```

Wire contract stays agent-friendly: `ReportService` gains an optional
`parent_external_id`; the ingest transaction resolves it to `parent_id`
server-side (agents never deal in server ids). The parent row is reported like
any other service, so ingest, correlation, and soft-removal all still work.

**Health rollup:** a parent's displayed health is derived from its children
(worst-of, or "healthy iff all healthy"), computed at read time in the same way
agent staleness already is — no new write path.

Reserve the logical-workload kinds (`deployment`, `statefulset`, `daemonset`)
when this lands. Doing this migration *before* the K8s agent, rather than
shipping K8s flat and re-modelling later, is the whole point of pinning it here.
