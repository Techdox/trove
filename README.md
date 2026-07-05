# Trove

[![CI](https://github.com/techdox/trove/actions/workflows/ci.yml/badge.svg)](https://github.com/techdox/trove/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/techdox/trove?sort=semver)](https://github.com/techdox/trove/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**One pane of glass for everything running in your homelab.** Small agents sit
next to your workloads — Docker hosts, Kubernetes clusters, Proxmox nodes,
plain Linux boxes — and push what they see to one server: what's running,
where, what version, whether it's healthy, whether its image is outdated, and
whether it's still reporting at all.

![Trove dashboard](docs/screenshot.png)

**Read-only by design.** Trove can never deploy, restart, exec into, or edit
anything. There is no code path that mutates a workload — agents only ever
issue read/list calls to their platforms. This is an architectural constraint,
not a feature toggle, and it's the project's one hard rule.

## Features

- **Service catalog across platforms** — containers, K8s workloads (with pods
  nested under their Deployments), Proxmox VMs/LXCs, and systemd units, all in
  one normalized view grouped by host.
- **Health + heartbeats** — platform health where it exists (Docker
  healthchecks, K8s readiness), plus server-side staleness: an agent that goes
  quiet flags itself and all its services within ~90 seconds.
- **Image freshness** — the server checks registries (batched, cached,
  rate-limit-aware) and badges services whose running image is behind its tag.
- **Alerts & digest** — instant notifications via webhook / Discord / ntfy when
  a host stops reporting, a service goes unhealthy or dies, or an image falls
  behind — with recovery notices, flap suppression, and a scheduled email
  digest. See [docs/alerts.md](docs/alerts.md).
- **State-change events** — a feed of started/stopped/unhealthy/appeared/removed
  (30-day retention, configurable).
- **Fast, dense dashboard** — no framework, keyboard-driven (`/` filter,
  `j`/`k` navigate, `enter` for details), auto-refreshing.
- **Trivial to operate** — one static binary (or container) per role, SQLite
  storage, automatic schema migrations, push-model agents that work from
  behind NAT.

## Quickstart (5 minutes)

Trove is a **server** (the dashboard + API) plus one **agent per platform** you
want to watch. The server is the same everywhere; only the agent differs. Start
with the compose file that matches what you're watching — each one stands up the
server *and* the right agent together. Requires Docker with Compose on the
machine that will host the dashboard.

**Docker host** — server + an agent watching this box's containers:

```sh
mkdir trove && cd trove
curl -fsSLO https://raw.githubusercontent.com/techdox/trove/main/examples/docker-compose.yml

export TROVE_TOKEN=trove_$(openssl rand -hex 24)
docker compose up -d
```

**Proxmox VE** — server + an agent watching your cluster's VMs and LXCs. First
create a read-only API token (the [Proxmox guide](docs/agents/proxmox.md) has
the exact `pveum` commands), then:

```sh
mkdir trove && cd trove
curl -fsSLO https://raw.githubusercontent.com/techdox/trove/main/examples/docker-compose.proxmox.yml

export TROVE_TOKEN=trove_$(openssl rand -hex 24)
export TROVE_PROXMOX_URL=https://YOUR-PVE-HOST:8006
export TROVE_PROXMOX_TOKEN='trove@pve!trove-agent=YOUR-TOKEN-SECRET'
docker compose -f docker-compose.proxmox.yml up -d
```

**Kubernetes** or **bare-metal Linux** — the agent doesn't run in Compose
(the K8s agent runs in-cluster as a Deployment; the bare-metal agent runs as a
systemd unit). Stand up the server with either compose file above, then deploy
the agent from the [Kubernetes](docs/agents/kubernetes.md) or
[bare-metal](docs/agents/local.md) guide.

Open <http://localhost:8080>. Your services appear within ~30 seconds. If an
agent doesn't show up, watch it connect with `docker compose logs -f agent`.

> ⚠️ The dashboard has **no authentication yet** — keep it on a trusted
> network (LAN/VPN/tailnet) or behind an authenticating reverse proxy. See
> [Security model](#security-model).

## Adding more hosts and platforms

Every agent needs its own token, minted on the server:

```sh
docker compose exec server trove-server agent create <name>
# e.g.: agent create docker-nas, agent create k8s-homelab, agent create proxmox
```

Then follow the guide for the platform:

| Platform                | Agent                 | Guide                                             |
| ----------------------- | --------------------- | ------------------------------------------------- |
| Docker host             | `trove-agent-docker`  | [docs/agents/docker.md](docs/agents/docker.md)    |
| Kubernetes cluster      | `trove-agent-k8s`     | [docs/agents/kubernetes.md](docs/agents/kubernetes.md) |
| Proxmox VE cluster      | `trove-agent-proxmox` | [docs/agents/proxmox.md](docs/agents/proxmox.md)  |
| Bare-metal Linux (systemd) | `trove-agent-local` | [docs/agents/local.md](docs/agents/local.md)      |

Container images (multi-arch amd64/arm64) live on GHCR:
`ghcr.io/techdox/trove-server`, `ghcr.io/techdox/trove-agent-docker`,
`ghcr.io/techdox/trove-agent-k8s`, `ghcr.io/techdox/trove-agent-proxmox`.
Static binaries for everything (including the bare-metal agent) are on the
[releases page](https://github.com/techdox/trove/releases).

## How it works

```
  docker host          k8s cluster         proxmox            nas (systemd)
 ┌────────────┐      ┌────────────┐      ┌────────────┐      ┌────────────┐
 │ agent      │      │ agent      │      │ agent      │      │ agent      │
 └─────┬──────┘      └─────┬──────┘      └─────┬──────┘      └─────┬──────┘
       │    POST /api/v1/report (Bearer token, every 30s)          │
       └───────────────┬───┴──────────────┬────────────────────────┘
                       ▼                  ▼
                  ┌─────────────────────────────┐
                  │ trove-server                │
                  │  SQLite · REST · dashboard  │
                  └─────────────────────────────┘
```

- **Push model**: agents POST full-state snapshots on an interval. The server
  never reaches into your infrastructure — homelab/NAT friendly.
- **Heartbeats**: miss 3 intervals → agent (and its services) marked *stale*;
  miss 10 → *offline*. Thresholds scale with each agent's own interval.
- **Full-state reports** are idempotent and tolerate lost pushes. Services
  that disappear are soft-removed and pruned after 24h.

## Server install options

**Docker Compose** — the quickstart above; data lives in the `trove-data` volume.

**Bare metal** — grab `trove-server` from a release archive and use
[deploy/systemd/trove-server.service](deploy/systemd/trove-server.service):

```sh
sudo install -m 0755 trove-server /usr/local/bin/
sudo cp deploy/systemd/trove-server.service /etc/systemd/system/
sudo systemctl enable --now trove-server
```

**Go install** (needs Go 1.26+):

```sh
go install github.com/techdox/trove/cmd/trove-server@latest
```

## Configuration reference

### `trove-server`

| Variable                   | Default    | Purpose                                                                |
| -------------------------- | ---------- | ---------------------------------------------------------------------- |
| `TROVE_ADDR`               | `:8080`    | Listen address.                                                         |
| `TROVE_DB`                 | `trove.db` | SQLite file path (containers default to `/data/trove.db`).             |
| `TROVE_FRESHNESS_ENABLED`  | `true`     | `false` disables image-freshness checking.                             |
| `TROVE_FRESHNESS_INTERVAL` | `5m`       | How often to scan for images due a check.                              |
| `TROVE_FRESHNESS_TTL`      | `6h`       | How long a resolved digest counts as fresh before rechecking.          |
| `TROVE_REGISTRY_AUTHS`     | _(unset)_  | Credentials for private registries — see below.                        |
| `TROVE_EVENT_RETENTION`    | `720h`     | How long events (activity feed / alert stream) are kept.               |
| `TROVE_REMOVED_RETENTION`  | `24h`      | How long removed services linger before being purged.                  |
| `TROVE_ALERT_*` / `TROVE_SMTP_*` | _(unset)_ | Notification channels & SMTP — see [docs/alerts.md](docs/alerts.md). |
| `TROVE_DIGEST`             | `daily@08:00`* | Digest schedule; *only takes effect once `TROVE_SMTP_*` is set — see [docs/alerts.md](docs/alerts.md). |
| `TROVE_BOOTSTRAP_AGENT` / `TROVE_BOOTSTRAP_TOKEN` | _(unset)_ | Seed one agent at startup (used by the quickstart compose). |

Private registry / Docker Hub credentials for freshness checks:

```sh
TROVE_REGISTRY_AUTHS='{"docker.io":{"username":"me","password":"dckr_pat_..."},"gitea.example.com":{"username":"me","password":"..."}}'
```

Docker Hub's anonymous rate limits are generous for Trove's batched, cached
checks at homelab scale, but if you run many distinct Hub images, adding a
(free) Hub account raises the ceiling.

### Agents — common to all

| Variable           | Default      | Purpose                                            |
| ------------------ | ------------ | -------------------------------------------------- |
| `TROVE_SERVER_URL` | _(required)_ | Base URL of the server.                            |
| `TROVE_TOKEN`      | _(required)_ | Bearer token from `trove-server agent create`.     |
| `TROVE_INTERVAL`   | `30s`        | Push interval (`30s`, `1m`, or bare seconds `30`). |
| `TROVE_AGENT_NAME` | hostname     | Informational; not used for the dashboard display name (see below). For the bare-metal agent specifically, it (or the OS hostname) becomes the reported host name. |

The name an agent appears under on the dashboard is the one you chose in
`trove-server agent create <name>` — not `TROVE_AGENT_NAME`. Platform-specific settings are covered in
each [agent guide](docs/agents/).

### Managing agents

```sh
trove-server agent create <name>    # mint a token (shown once, stored hashed)
trove-server agent list             # names, platform, status, last seen
trove-server agent delete <name>    # remove an agent and all its data
trove-server alert test             # push a test notification through configured channels
```

## API

| Method & path           | Auth   | Purpose                                     |
| ----------------------- | ------ | ------------------------------------------- |
| `POST /api/v1/report`   | Bearer | Agent pushes a full-state report.           |
| `GET /api/v1/services`  | none   | Services grouped by host (dashboard data).  |
| `GET /api/v1/agents`    | none   | Agents with derived heartbeat status.       |
| `GET /api/v1/events`    | none   | Recent state-change events (`?limit=`).     |
| `GET /healthz`          | none   | Liveness + database reachability.           |

The wire contract lives in [`pkg/model`](pkg/model/model.go) — the one package
agents import. Building an agent for a new platform means implementing one
interface; see [CONTRIBUTING.md](CONTRIBUTING.md).

## Security model

- Agent ingest is authenticated with per-agent bearer tokens (256-bit random,
  stored only as SHA-256 hashes). Revoke by deleting the agent.
- **The dashboard and read APIs have no authentication in this phase.** Treat
  the server like any internal tool: trusted network only, or front it with an
  authenticating reverse proxy. Native OIDC is on the roadmap.
- Agents cannot change anything on the platforms they watch — read-only is
  enforced in code, not convention. Details in [SECURITY.md](SECURITY.md).

## Upgrades & backup

- **Upgrade**: pull the new image (or binary) and restart. Schema migrations
  run automatically on startup; agents and server tolerate version skew within
  a minor version.
- **Backup**: everything is one SQLite file (`trove.db` / the `trove-data`
  volume). Copy it while the server is stopped, or use `sqlite3 ... ".backup"`
  live. Trove state is rebuildable anyway — agents repopulate the catalog
  within one interval; you'd lose only event history.

## Building from source

```sh
git clone https://github.com/techdox/trove.git && cd trove
make native   # all binaries for your host platform → bin/
make build    # cross-compile linux amd64+arm64
make test     # go test ./...
docker compose up --build   # contributor dev stack
```

Pure Go, no CGO, no frontend build step — the dashboard is vanilla JS embedded
into the server binary.

## Roadmap & contributing

Planned next: OIDC / dashboard auth, Helm chart, cert-expiry monitoring — see
[ROADMAP.md](ROADMAP.md) for the reasoning and sequencing. Contributions
welcome: start with [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE) © Techdox
