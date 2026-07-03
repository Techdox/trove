# Trove

A read-only service catalog and health monitor. Agents run next to your
workloads, discover what's running, and push periodic reports to a central
server. The server gives you one pane of glass: what's running, where, what
version, whether it's healthy, and whether it's still reporting.

**Read-only by design.** Trove can never deploy, restart, exec into, or edit
anything. There is no code path that mutates a workload — the Docker agent only
issues `GET` requests to the Docker Engine API. This is an architectural
constraint, not a feature toggle.

> **Phase 1** supports Docker hosts. Kubernetes, Proxmox, and bare-metal agents,
> image-freshness checks, alerting, and OIDC are planned for later phases (see
> [Roadmap](#roadmap)).

---

## How it works

```
   ┌─────────────┐   POST /api/v1/report    ┌──────────────────────┐
   │ docker agent │ ───────────────────────▶ │  trove-server        │
   │  (per host)  │      (Bearer token)      │  ├─ SQLite           │
   └─────────────┘                           │  ├─ REST API         │
   ┌─────────────┐                           │  └─ embedded SPA     │
   │ docker agent │ ───────────────────────▶ │                      │
   └─────────────┘                           └──────────┬───────────┘
                                                         │ GET /  (dashboard)
                                                         ▼
                                                    your browser
```

- **Push model.** Agents POST to the server on an interval (default 30s). This
  is NAT/homelab friendly — the server never needs to reach back to an agent.
- **Heartbeats.** The server tracks each agent's last report. An agent that
  goes quiet for 3 intervals is **stale**; for 10 intervals it's **offline**.
  Its services are flagged stale automatically.
- **Full-state reports.** Each report is a complete snapshot, not a delta —
  idempotent and tolerant of lost pushes. Services that disappear from a report
  are soft-removed and pruned after 24h.

## Quick start (local dev)

Requires Docker with Compose.

```sh
docker compose up --build
```

Then open <http://localhost:8080>. Within a minute the dashboard shows the
containers running on your machine, grouped by host, with state and health.

The dev stack auto-creates an agent using a shared token baked into
`docker-compose.yml` (via `TROVE_BOOTSTRAP_*`). **This is for local dev only** —
see [Production](#production) for real deployments.

Tear down (including the database volume):

```sh
docker compose down -v
```

## Building

Pure Go — no CGO — so binaries are static and cross-compile cleanly.

```sh
make build     # cross-compile both binaries for linux/amd64 + linux/arm64 → bin/
make native    # build for the host platform → bin/
make test      # run tests
make help      # list all targets
```

## Production

### 1. Run the server

Run `trove-server` behind whatever you use for TLS/ingress. It listens on
`TROVE_ADDR` (default `:8080`) and stores everything in the SQLite file at
`TROVE_DB` (default `trove.db`).

> ⚠️ **The dashboard and read APIs have no authentication in Phase 1.** Only the
> agent ingest endpoint is authenticated (bearer token). **Bind the server to
> localhost or a trusted network, or put it behind an authenticating reverse
> proxy.** Do not expose it directly to the internet. OIDC is planned for a
> later phase.

Leave `TROVE_BOOTSTRAP_*` unset in production.

### 2. Mint an agent token

Tokens are generated server-side and stored only as a SHA-256 hash. The
plaintext is shown once:

```sh
trove-server agent create docker-nuc01
# or, against the compose stack:
docker compose exec server trove-server agent create docker-nuc01
```

Other agent management:

```sh
trove-server agent list             # names, platform, status, last seen
trove-server agent delete <name>    # removes the agent and all its data
```

### 3. Run an agent on each Docker host

```sh
docker run -d --restart unless-stopped \
  -e TROVE_SERVER_URL=https://trove.example.internal \
  -e TROVE_TOKEN=trove_xxxxxxxx \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  trove-agent-docker
```

## Configuration

### `trove-server`

| Variable                | Default      | Purpose                                              |
| ----------------------- | ------------ | ---------------------------------------------------- |
| `TROVE_ADDR`            | `:8080`      | Listen address.                                      |
| `TROVE_DB`              | `trove.db`   | SQLite file path.                                    |
| `TROVE_BOOTSTRAP_AGENT` | _(unset)_    | Dev only: seed an agent of this name at startup.     |
| `TROVE_BOOTSTRAP_TOKEN` | _(unset)_    | Dev only: token for the bootstrapped agent.          |

### `trove-agent-docker`

| Variable            | Default                        | Purpose                                            |
| ------------------- | ------------------------------ | -------------------------------------------------- |
| `TROVE_SERVER_URL`  | _(required)_                   | Base URL of the server.                            |
| `TROVE_TOKEN`       | _(required)_                   | Bearer token from `agent create`.                  |
| `TROVE_INTERVAL`    | `30s`                          | Push interval (`30s`, `1m`, or bare seconds `30`). |
| `TROVE_AGENT_NAME`  | hostname                       | Name reported to the server.                       |
| `DOCKER_HOST`       | `unix:///var/run/docker.sock`  | Docker endpoint (`unix://` or `tcp://`).           |

## API

| Method & path            | Auth   | Purpose                                    |
| ------------------------ | ------ | ------------------------------------------ |
| `POST /api/v1/report`    | Bearer | Agent pushes a full-state report.          |
| `GET  /api/v1/services`  | none   | Services grouped by host (dashboard data). |
| `GET  /api/v1/agents`    | none   | Agents with derived heartbeat status.      |
| `GET  /api/v1/events`    | none   | Recent state-change events (`?limit=`).    |
| `GET  /healthz`          | none   | Liveness + database reachability.          |

The report payload contract lives in [`pkg/model`](pkg/model/model.go) — the
one package agents import.

## Project layout

```
cmd/
  trove-server/          # server + agent-token CLI
  trove-agent-docker/    # Docker agent (read-only Engine API client)
internal/
  server/                # HTTP handlers, auth middleware, staleness ticker
  store/                 # SQLite: schema, migrations, ingest, queries
  staleness/             # pure heartbeat evaluation
pkg/
  model/                 # shared wire types (agents import this)
web/                     # dashboard SPA, embedded into the server binary
```

## Data & retention

State lives in one SQLite file. Trove keeps the latest state per service plus a
rolling 24h of state-change events; anything older is pruned on write. There is
no unbounded history in Phase 1.

## Roadmap

Deferred to later phases, deliberately not built yet:

- Kubernetes / Proxmox / bare-metal agents (the `services` schema already
  reserves `pod`, `vm`, `lxc`, `process` kinds).
- Image-freshness checks (the agent already captures registry digests).
- Alerts / webhooks / email reports.
- OIDC / dashboard authentication.
- Helm chart.
```
