# Trove

A read-only service catalog and health monitor. Agents run next to your
workloads, discover what's running, and push periodic reports to a central
server. The server gives you one pane of glass: what's running, where, what
version, whether it's healthy, and whether it's still reporting.

**Read-only by design.** Trove can never deploy, restart, exec into, or edit
anything. There is no code path that mutates a workload — every agent only ever
issues read/list calls to its platform. This is an architectural constraint, not
a feature toggle.

**Agents:** Docker, Kubernetes, Proxmox, and bare-metal (systemd) hosts.
**Also:** per-image freshness (is the running image behind its registry tag?).
Alerting, email reports, and OIDC are planned for later phases (see
[ROADMAP.md](ROADMAP.md)).

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
- **One model for everything.** A Docker container, a Proxmox VM, a Kubernetes
  Deployment, a systemd unit — all normalize to a *service*. Kubernetes
  Deployments/StatefulSets/DaemonSets are parents; their Pods are child
  instances nested beneath them in the dashboard.
- **Image freshness.** The server periodically resolves the latest manifest
  digest for each image's tag from its registry (batched + cached, backoff-aware)
  and flags whether the running image is `current` or `outdated`.

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

### 3. Run agents

Mint one token per agent (`trove-server agent create <name>`), then run the
agent for the platform. All agents share `TROVE_SERVER_URL`, `TROVE_TOKEN`,
`TROVE_INTERVAL`, and `TROVE_AGENT_NAME`; each adds its own.

**Docker** — one per host, reads the socket read-only:

```sh
docker run -d --restart unless-stopped \
  -e TROVE_SERVER_URL=https://trove.example.internal \
  -e TROVE_TOKEN=trove_xxxxxxxx \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  trove-agent-docker
```

**Kubernetes** — one per cluster, in-cluster with a read-only ClusterRole. Build
the image and apply the manifest:

```sh
docker build -f Dockerfile.agents --build-arg CMD=trove-agent-k8s -t <registry>/trove-agent-k8s:0.2.0 .
# edit image + TROVE_SERVER_URL + TROVE_CLUSTER_NAME, create the token secret, then:
kubectl apply -f deploy/kubernetes/trove-agent.yaml
```

**Proxmox** — one per cluster, uses a read-only API token:

```sh
docker build -f Dockerfile.agents --build-arg CMD=trove-agent-proxmox -t trove-agent-proxmox .
docker run -d --restart unless-stopped \
  -e TROVE_SERVER_URL=https://trove.example.internal \
  -e TROVE_TOKEN=trove_xxxxxxxx \
  -e TROVE_PROXMOX_URL=https://pve.example:8006 \
  -e TROVE_PROXMOX_TOKEN='user@pam!trove=xxxxxxxx-xxxx-...' \
  -e TROVE_PROXMOX_INSECURE=true \
  trove-agent-proxmox
```

**Bare-metal (systemd)** — runs as a host binary (it reads the host's systemd);
see [`deploy/systemd/trove-agent-local.service`](deploy/systemd/trove-agent-local.service).

## Configuration

### `trove-server`

| Variable                | Default      | Purpose                                              |
| ----------------------- | ------------ | ---------------------------------------------------- |
| `TROVE_ADDR`               | `:8080`      | Listen address.                                              |
| `TROVE_DB`                 | `trove.db`   | SQLite file path.                                            |
| `TROVE_FRESHNESS_ENABLED`  | `true`       | Set `false` to disable image-freshness checking.            |
| `TROVE_FRESHNESS_INTERVAL` | `5m`         | How often to scan for images due a check.                   |
| `TROVE_FRESHNESS_TTL`      | `6h`         | How long a resolved digest is treated as current.           |
| `TROVE_REGISTRY_AUTHS`     | _(unset)_    | JSON `{"host":{"username":..,"password":..}}` for private registries. |
| `TROVE_BOOTSTRAP_AGENT`    | _(unset)_    | Dev only: seed an agent of this name at startup.            |
| `TROVE_BOOTSTRAP_TOKEN`    | _(unset)_    | Dev only: token for the bootstrapped agent.                 |

### Agents — common

| Variable            | Default        | Purpose                                            |
| ------------------- | -------------- | -------------------------------------------------- |
| `TROVE_SERVER_URL`  | _(required)_   | Base URL of the server.                            |
| `TROVE_TOKEN`       | _(required)_   | Bearer token from `agent create`.                  |
| `TROVE_INTERVAL`    | `30s`          | Push interval (`30s`, `1m`, or bare seconds `30`). |
| `TROVE_AGENT_NAME`  | hostname       | Name reported to the server.                       |

### Per-agent

| Agent      | Variable                 | Purpose                                                        |
| ---------- | ------------------------ | -------------------------------------------------------------- |
| docker     | `DOCKER_HOST`            | Docker endpoint (default `unix:///var/run/docker.sock`).       |
| kubernetes | `TROVE_CLUSTER_NAME`     | Trove host name for the cluster (default `kubernetes`).        |
| kubernetes | `TROVE_KUBE_NAMESPACE`   | Scope to one namespace (default: all).                         |
| kubernetes | `TROVE_KUBE_APISERVER` / `TROVE_KUBE_TOKEN` / `TROVE_KUBE_CA` / `TROVE_KUBE_INSECURE` | Out-of-cluster overrides (in-cluster auto-detected). |
| proxmox    | `TROVE_PROXMOX_URL`      | Proxmox API base, e.g. `https://pve:8006` (required).          |
| proxmox    | `TROVE_PROXMOX_TOKEN`    | API token `USER@REALM!TOKENID=SECRET` (required).              |
| proxmox    | `TROVE_PROXMOX_INSECURE` | `true` to skip TLS verification (self-signed certs).           |
| local      | `TROVE_LOCAL_UNIT_FILTER`| Glob to select units (e.g. `docker*`); default all.            |
| local      | `TROVE_LOCAL_ALL`        | `true` to include inactive units (default: active/failed only).|

## API

| Method & path            | Auth   | Purpose                                    |
| ------------------------ | ------ | ------------------------------------------ |
| `POST /api/v1/report`    | Bearer | Agent pushes a full-state report.          |
| `GET  /api/v1/services`  | none   | Services grouped by host (dashboard data). |
| `GET  /api/v1/agents`    | none   | Agents with derived heartbeat status.      |
| `GET  /api/v1/events`    | none   | Recent state-change events (`?limit=`).    |
| `GET  /healthz`          | none   | Liveness + database reachability.          |

`GET /api/v1/services` returns services grouped by host; each carries
`freshness` (`current`/`outdated`/`unknown`) and, for child instances,
`parent_external_id`. The report payload contract lives in
[`pkg/model`](pkg/model/model.go) — the one package agents import.

## Project layout

```
cmd/
  trove-server/          # server + agent-token CLI
  trove-agent-docker/    # Docker agent (read-only Engine API client)
  trove-agent-k8s/       # Kubernetes agent (read-only API client)
  trove-agent-proxmox/   # Proxmox VE agent (read-only API token)
  trove-agent-local/     # bare-metal agent (systemd units, read-only)
internal/
  agentkit/              # shared agent config, push client, run loop
  server/                # HTTP handlers, auth, staleness + freshness tickers
  store/                 # SQLite: schema, migrations, ingest, queries
  staleness/             # pure heartbeat evaluation
  registry/              # image-freshness registry client (Docker Registry v2)
pkg/
  model/                 # shared wire types (agents import this)
web/                     # dashboard SPA, embedded into the server binary
deploy/                  # k8s manifest + systemd unit for the agents
```

## Data & retention

State lives in one SQLite file. Trove keeps the latest state per service plus a
rolling 24h of state-change events; anything older is pruned on write. There is
no unbounded history in Phase 1.

## Roadmap

Delivered: Docker/Kubernetes/Proxmox/bare-metal agents and image freshness.
Still deferred (see [ROADMAP.md](ROADMAP.md)):

- Alerts / webhooks / email reports (needs the retention decision — D1).
- OIDC / dashboard authentication.
- Helm chart.
```
