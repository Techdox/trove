# Trove Roadmap

Phase 1 (the current `main`) ships a read-only Docker service catalog: push-model
agents, per-agent token auth, heartbeat/staleness, and an embedded dashboard.
See the [README](README.md) for what exists today.

This document sequences the deferred work and — more importantly — pins the two
decisions that must be made *before* the phase that needs them, because getting
them wrong means a migration or a rewrite later.

## Principles carried forward

Every phase must preserve the Phase 1 invariants:

- **Read-only, always.** No phase adds a deploy/restart/exec/edit path.
- **Everything normalizes into `services`.** New platforms map their world onto
  the existing model rather than growing parallel tables.
- **Push model, full-state reports, idempotent ingest.** New agents reuse the
  `pkg/model` contract and the ingest transaction.
- **Single static binary, pure Go.** No CGO, no per-platform build complexity.

---

## Phase 2 — Image freshness

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

## Phase 3 — Additional agents (K8s, Proxmox, bare metal)

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

## Phase 4 — Notifications & reporting

**Goal:** stop needing to watch the dashboard.

- **Alerts / webhooks** fired on state transitions. The `events` table already
  records transitions — but see **[Decision D1](#d1--retention--history)**:
  today they're pruned at 24h, which is too aggressive for reliable alerting and
  digests.
- **Email reports:** scheduled digests (daily/weekly summary, what changed).
- **Cert monitoring:** track TLS expiry for services that expose it.

Depends on the retention decision landing first.

---

## Phase 5 — Auth & packaging

**Goal:** make Trove safe to expose beyond a trusted network, and easy to deploy.

- **OIDC** on the dashboard and read APIs. Phase 1 intentionally has **no auth**
  (README says bind to a trusted network / front with an authenticating proxy);
  this closes that gap. A reverse-proxy (oauth2-proxy) deployment is the interim
  answer until native OIDC lands.
- **Helm chart** for Kubernetes deployment (natural companion to the Phase 3 K8s
  agent).

---

## Decisions to make before they bite

These two ripple across later phases. Deciding them once, early, is far cheaper
than migrating twice.

### D1 — Retention & history

**Problem:** Phase 1 keeps only the latest state per service plus a 24h rolling
event log, pruned on the write path. That is deliberately minimal and is fine
for a live dashboard, but Phase 4 breaks against it: alerting can't miss events,
and weekly digests need more than 24h of history.

**Proposed direction:**
- Make retention **configurable** (e.g. `TROVE_EVENT_RETENTION`, default 24h),
  and raise it (30–90d) once reporting exists.
- **Move pruning off the write path** to a periodic maintenance job once volume
  grows — pruning inside every ingest transaction won't scale to many agents.
- Keep `events` as the append-only source of truth; add downsampling/rollups
  only if raw volume becomes a problem.
- Each new cache (the Phase 2 `image_checks` table) gets its *own* retention;
  don't couple them.

### D2 — Parent/child schema

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
