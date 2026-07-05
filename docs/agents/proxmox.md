# Proxmox agent

Watches a Proxmox VE cluster and reports every VM (`qemu`) and LXC container,
grouped by node. Templates are skipped. Read-only: the agent authenticates
with an API token that has only the `PVEAuditor` role and issues only `GET`s.

Run **one agent per cluster** (it discovers all nodes through any one API
endpoint).

**Where things run:** you need a Trove **server** running somewhere first (see
the [Quickstart](../../README.md#quickstart-5-minutes)). The Proxmox **agent**
runs wherever the server does — a NAS, a small VM, any Linux box or container —
**not** on a PVE node. `TROVE_PROXMOX_URL` is your PVE cluster's API address as
seen from wherever the agent runs.

## 1. Create a read-only API token in Proxmox

Run these as root on any PVE node's shell (or use the web UI under Datacenter →
Permissions):

```sh
# a dedicated user for trove
pveum user add trove@pve --comment "Trove read-only catalog agent"

# audit-only rights across the datacenter (read everything, change nothing)
pveum aclmod / --users trove@pve --roles PVEAuditor

# an API token for that user; --privsep 0 makes the token inherit the
# user's (audit-only) permissions
pveum user token add trove@pve trove-agent --privsep 0
```

> **`--privsep 0` is required.** A privilege-separated token — the default for
> both `pveum` and the web UI (where "Privilege Separation" is a checked box) —
> starts with **no** permissions of its own. Such a token still *authenticates*,
> so the agent connects and looks healthy, but every Proxmox query comes back
> empty and **no guests ever appear on the dashboard, with no error.** If you
> use the web UI, uncheck "Privilege Separation."

The `pveum user token add` output is a table with two fields you must combine.
Glue the `full-tokenid` (e.g. `trove@pve!trove-agent`) and the `value` (a UUID)
with an `=` — that whole string is your `TROVE_PROXMOX_TOKEN`:

```
trove@pve!trove-agent=<the-secret-uuid>
```

**Verify the token before going further** — this catches the empty-permissions
trap above in seconds (run it from wherever the agent will run):

```sh
curl -sk -H "Authorization: PVEAPIToken=trove@pve!trove-agent=<secret>" \
  https://YOUR-PVE-HOST:8006/api2/json/nodes
```

You should get a JSON list of your nodes. `{"data":[]}` means the token
authenticated but has no permission — recheck the `aclmod` and `--privsep 0`
steps.

## 2. Get a Trove agent token

**Using the Compose file in step 3?** It generates and seeds this token for you
from `TROVE_TOKEN` — skip to step 3.

**Running the container by hand (server already exists)?** Mint one on the
server:

```sh
# Docker Compose server:
docker compose exec server trove-server agent create proxmox
# bare-metal server: sudo TROVE_DB=/var/lib/trove/trove.db trove-server agent create proxmox
```

## 3. Run the agent

### With Docker Compose (server + agent together)

If you don't already have a Trove server, the quickstart Compose file stands up
both at once — no cloning or building:

```sh
curl -fsSLO https://raw.githubusercontent.com/techdox/trove/main/examples/docker-compose.proxmox.yml

# Save settings to .env (Compose loads it automatically; it persists across
# restarts). Then edit .env to fill in your real PVE host and API token.
{
  echo "TROVE_TOKEN=trove_$(openssl rand -hex 24)"
  echo "TROVE_PROXMOX_URL=https://YOUR-PVE-HOST:8006"
  echo "TROVE_PROXMOX_TOKEN=trove@pve!trove-agent=YOUR-TOKEN-SECRET"
} > .env
docker compose -f docker-compose.proxmox.yml up -d
docker compose -f docker-compose.proxmox.yml logs -f agent   # watch it connect
```

> The agent image is `trove-agent-proxmox` — **not** `trove-agent-docker`. The
> Docker agent ignores the `TROVE_PROXMOX_*` variables and reads the Docker
> socket instead, so it will connect and look healthy while never contacting
> Proxmox. If you adapt your own compose file, make sure the agent uses the
> Proxmox image (and it needs no `/var/run/docker.sock` mount).

### Against an existing server (`docker run`)

```sh
docker run -d --name trove-agent-proxmox --restart unless-stopped \
  -e TROVE_SERVER_URL=http://YOUR-SERVER:8080 \
  -e TROVE_TOKEN=trove_xxxxxxxx \
  -e TROVE_PROXMOX_URL=https://YOUR-PVE-HOST:8006 \
  -e TROVE_PROXMOX_TOKEN='trove@pve!trove-agent=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  -e TROVE_PROXMOX_INSECURE=true \
  ghcr.io/techdox/trove-agent-proxmox:latest
```

> **Two different tokens, don't mix them up.** `TROVE_TOKEN` is Trove's own
> agent token (format `trove_…`, from step 2). `TROVE_PROXMOX_TOKEN` is your
> Proxmox API token (format `user@realm!tokenid=secret`, from step 1).

`TROVE_PROXMOX_INSECURE=true` skips TLS verification — needed for Proxmox's
default self-signed certificate. Drop it if your PVE API has a real cert.

Replace `YOUR-SERVER` and `YOUR-PVE-HOST` with addresses reachable from where
the agent runs (not `localhost` unless the agent shares that host).

## Configuration

| Variable                 | Default      | Purpose                                          |
| ------------------------ | ------------ | ------------------------------------------------ |
| `TROVE_SERVER_URL`       | _(required)_ | Base URL of the Trove server.                    |
| `TROVE_TOKEN`            | _(required)_ | Trove agent token (`trove_…`), from step 2.      |
| `TROVE_PROXMOX_URL`      | _(required)_ | PVE API base, e.g. `https://pve.lan:8006`.       |
| `TROVE_PROXMOX_TOKEN`    | _(required)_ | `USER@REALM!TOKENID=SECRET` from step 1.         |
| `TROVE_PROXMOX_INSECURE` | `false`      | `true` to accept self-signed certificates.       |
| `TROVE_INTERVAL`         | `30s`        | Push interval.                                   |

## What you'll see

Each Proxmox node appears as a host; its VMs and LXCs are services with
`running`/`stopped` state. The dashboard's Image column shows the guest OS
reported by Proxmox config (`ostype`) where available — for example `Windows
11`, `Linux`, `Debian`, or `Ubuntu`. This uses only read-only config endpoints;
it does not require the QEMU guest agent. Proxmox has no app-level healthcheck,
so health shows `unknown` — the state badge carries the up/down signal, and
Trove's agent heartbeat covers "is the cluster still reporting at all."

## Nothing showing up?

Watch the agent's logs (`docker compose -f docker-compose.proxmox.yml logs -f
agent`, or `docker logs trove-agent-proxmox`):

- **`collect failed` with an auth/connection error** — the agent can't reach or
  authenticate to the PVE API. Check `TROVE_PROXMOX_URL` is reachable from the
  agent's host and `TROVE_PROXMOX_TOKEN` is correct (and `TROVE_PROXMOX_INSECURE=true`
  for a self-signed cert).
- **`collected 0 hosts …` warning** — the token authenticated but returned no
  nodes: it's missing the `PVEAuditor` role or was created with privilege
  separation. Re-run the step-1 `aclmod` / `--privsep 0` commands, or use the
  curl pre-flight test above.
- **`push failed … 401`** — `TROVE_TOKEN` doesn't match a Trove agent on the
  server (e.g. you pasted the Proxmox token here by mistake, or minted the
  token against a different database).
- **The agent shows on the dashboard but has no guests** — same as the
  `collected 0 hosts` case above.
