# Proxmox agent

Watches a Proxmox VE cluster and reports every VM (`qemu`) and LXC container,
grouped by node. Templates are skipped. Read-only: the agent authenticates
with an API token that has only the `PVEAuditor` role and issues only `GET`s.

Run **one agent per cluster** (it discovers all nodes through any one API
endpoint).

## 1. Create a read-only API token in Proxmox

On any PVE node's shell (or via the web UI under Datacenter → Permissions):

```sh
# a dedicated user for trove
pveum user add trove@pve --comment "Trove read-only catalog agent"

# audit-only rights across the datacenter (read everything, change nothing)
pveum aclmod / --users trove@pve --roles PVEAuditor

# an API token for that user; --privsep 0 makes the token inherit the
# user's (audit-only) permissions
pveum user token add trove@pve trove-agent --privsep 0
```

The output shows the token secret once. Your token string for Trove is:

```
trove@pve!trove-agent=<the-secret-uuid>
```

## 2. Mint a Trove token

```sh
trove-server agent create proxmox-cluster
```

## 3. Run the agent

Anywhere that can reach both the Proxmox API and the Trove server — commonly
as a container on the same box as the Trove server:

```sh
docker run -d --name trove-agent-proxmox --restart unless-stopped \
  -e TROVE_SERVER_URL=http://trove.lan:8080 \
  -e TROVE_TOKEN=trove_xxxxxxxx \
  -e TROVE_PROXMOX_URL=https://pve.lan:8006 \
  -e TROVE_PROXMOX_TOKEN='trove@pve!trove-agent=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  -e TROVE_PROXMOX_INSECURE=true \
  ghcr.io/techdox/trove-agent-proxmox:latest
```

`TROVE_PROXMOX_INSECURE=true` skips TLS verification — needed for Proxmox's
default self-signed certificate. Drop it if your PVE API has a real cert.

### Or with Docker Compose (server + agent together)

If you don't already have a Trove server running, the quickstart compose file
stands up both at once — no cloning or building:

```sh
curl -fsSLO https://raw.githubusercontent.com/techdox/trove/main/examples/docker-compose.proxmox.yml

export TROVE_TOKEN=trove_$(openssl rand -hex 24)
export TROVE_PROXMOX_URL=https://YOUR-PVE-HOST:8006
export TROVE_PROXMOX_TOKEN='trove@pve!trove-agent=YOUR-TOKEN-SECRET'
docker compose -f docker-compose.proxmox.yml up -d
docker compose -f docker-compose.proxmox.yml logs -f agent   # watch it connect
```

> The agent image is `trove-agent-proxmox` — **not** `trove-agent-docker`. The
> Docker agent ignores the `TROVE_PROXMOX_*` variables and reads the Docker
> socket instead, so it will connect and look healthy while never contacting
> Proxmox. If you're adapting your own compose file, make sure the agent uses
> the Proxmox image (and it needs no `/var/run/docker.sock` mount).

## Configuration

| Variable                 | Default      | Purpose                                          |
| ------------------------ | ------------ | ------------------------------------------------ |
| `TROVE_SERVER_URL`       | _(required)_ | Base URL of the Trove server.                    |
| `TROVE_TOKEN`            | _(required)_ | Token from `agent create`.                       |
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
