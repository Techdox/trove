# Configuration reference

Environment variables for `trove-server` and the agents. Install and
quickstart commands stay in the [root README](../README.md). Authentication
setup is in [Dashboard authentication](authentication.md).

## `trove-server`

| Variable                   | Default    | Purpose                                                                |
| -------------------------- | ---------- | ---------------------------------------------------------------------- |
| `TROVE_ADDR`               | `:8080`    | Listen address.                                                         |
| `TROVE_DB`                 | `trove.db` | SQLite file path (containers default to `/data/trove.db`).             |
| `TROVE_FRESHNESS_ENABLED`  | `true`     | `false` disables image-freshness checking.                             |
| `TROVE_FRESHNESS_INTERVAL` | `5m`       | How often to scan for images due a check.                              |
| `TROVE_FRESHNESS_TTL`      | `6h`       | How long a resolved digest counts as fresh before rechecking.          |
| `TROVE_REGISTRY_AUTHS`     | _(unset)_  | Credentials for private registries — see below.                        |
| `TROVE_REGISTRY_PRIVATE_HOSTS` | _(unset)_ | Comma-separated private registry `host[:port]` allowlist. Hosts in `TROVE_REGISTRY_AUTHS` are allowed automatically. |
| `TROVE_HEALTH_DETAILS_ENABLED` | `false` | Explicitly retain and display bounded, redacted platform health messages. |
| `TROVE_EVENT_RETENTION`    | `720h` (30d) | How long events (activity feed / alert stream) are kept.             |
| `TROVE_REMOVED_RETENTION`  | `24h`      | How long removed services linger before being purged.                  |
| `TROVE_HOST_RETENTION`     | `720h` (30d) | How long a silent host and its remaining inventory are retained.     |
| `TROVE_ALERT_*` / `TROVE_SMTP_*` | _(unset)_ | Notification channels & SMTP — see [alerts.md](alerts.md). |
| `TROVE_DIGEST`             | `daily@08:00`* | Digest schedule; *only takes effect once `TROVE_SMTP_*` is set — see [alerts.md](alerts.md). |
| `TROVE_BOOTSTRAP_AGENT` / `TROVE_BOOTSTRAP_TOKEN` | _(unset)_ | Seed one agent at startup (used by the quickstart compose). |

### Dashboard authentication (OIDC)

By default the dashboard and APIs are open — bind to a trusted network or
front with a reverse proxy. Native OIDC is optional. See
[Dashboard authentication](authentication.md) for the reverse-proxy path,
provider setup, and verification.

| Variable | Purpose |
| --- | --- |
| `TROVE_OIDC_ISSUER` | OIDC discovery URL, e.g. `https://auth.example/application/o/trove/` |
| `TROVE_OIDC_CLIENT_ID` | OAuth2 client ID registered with your IdP |
| `TROVE_OIDC_CLIENT_SECRET` | OAuth2 client secret |
| `TROVE_OIDC_REDIRECT_URL` | Callback URL, e.g. `https://trove.example/oauth2/callback` |
| `TROVE_API_TOKEN` | _(optional)_ Random bearer token of at least 32 characters for programmatic API access (bypasses OIDC) |
| `TROVE_OIDC_SESSION_MAX_AGE` | _(optional)_ Session duration (default `8h`) |

OIDC is enabled only when all four required `TROVE_OIDC_*` settings are
present. If any required setting is present while another is missing, the
server fails startup and names the missing variables instead of leaving the
dashboard open. `TROVE_API_TOKEN` is valid only alongside a complete OIDC
configuration.

Generate the optional API token with `openssl rand -hex 32`; Trove rejects
short tokens and known documentation placeholders at startup. Agent ingest
(`POST /api/v1/report`) and `/healthz` remain outside OIDC.

### Private registries

```sh
TROVE_REGISTRY_AUTHS='{"docker.io":{"username":"me","password":"dckr_pat_..."},"gitea.example.com":{"username":"me","password":"...","auth_realm_hosts":["sso.example.com"]}}'
```

Private IP ranges are denied by default. A host configured in
`TROVE_REGISTRY_AUTHS` is an explicit private-network allowlist entry. For an
anonymous private registry, set its exact endpoint separately, for example
`TROVE_REGISTRY_PRIVATE_HOSTS=registry.lan:5000`. Loopback, link-local/cloud
metadata, unspecified, and multicast destinations remain blocked even when
listed. Registry credentials are sent to a separate bearer-token realm only
when that realm is the registry itself, Docker Hub's standard auth service, or
an exact `auth_realm_hosts` entry. Those explicitly trusted realm hosts are
also eligible to resolve to a private address.

Docker Hub's anonymous rate limits are generous for Trove's batched, cached
checks at homelab scale, but if you run many distinct Hub images, adding a
(free) Hub account raises the ceiling.

## Agents — common to all

| Variable           | Default      | Purpose                                            |
| ------------------ | ------------ | -------------------------------------------------- |
| `TROVE_SERVER_URL` | _(required)_ | Base URL of the server.                            |
| `TROVE_TOKEN`      | _(required)_ | Bearer token from Add agent or `trove-server agent create`. |
| `TROVE_INTERVAL`   | `30s`        | Push interval (`30s`, `1m`, or bare seconds `30`). |
| `TROVE_AGENT_NAME` | hostname     | Informational; not used for the dashboard display name (see below). For the bare-metal agent specifically, it (or the OS hostname) becomes the reported host name. |

The name an agent appears under on the dashboard is the one you chose when
creating it — not `TROVE_AGENT_NAME`. Platform-specific settings are covered in
each [agent guide](agents/).

## Managing agents

The dashboard **Add agent** button mints a token and shows a copy-paste
snippet for Docker Compose, Kubernetes, Proxmox, or systemd. The CLI still
works for the same job, including rotation and deletion.

Deleting an agent is an intentional server-side catalogue cleanup. It does not
stop or change anything on the infrastructure the agent used to observe.

```sh
trove-server agent create <name>    # mint a token (shown once, stored hashed)
trove-server agent list             # names, platform, status, last seen
trove-server agent rotate <name>    # replace a token without deleting history
trove-server agent delete <name>    # remove an agent and all its data
trove-server alert test             # test every configured channel + send a sample digest
```

On a Docker Compose server, run these inside the container, e.g.
`docker compose exec server trove-server agent create <name>`. On a bare-metal
server, set `TROVE_DB` to the server's database path.

`POST /api/v1/agents` is the same mint operation the dashboard uses. It
requires the same auth as the other dashboard APIs: an OIDC session or
`TROVE_API_TOKEN` when OIDC is enabled, and the trusted-network default when
it is not. Creating an agent only writes Trove's catalogue. It never talks to
Docker, Kubernetes, Proxmox, or systemd.
