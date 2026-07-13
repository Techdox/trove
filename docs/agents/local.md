# Bare-metal (systemd) agent

Watches a plain Linux host's systemd service units — the box that isn't
running Docker or Kubernetes but still matters. Read-only: it only ever runs
`systemctl list-units` (a query command).

Runs directly on the host (not in a container), since it needs the host's
systemd. Ships as a static binary in the release archives.

**Where things run:** you need a Trove **server** running and reachable first
(see the [Quickstart](../../README.md#quickstart-5-minutes)). This agent runs on
the host you want to watch and pushes to that server.

## 1. Mint a token

On the server (this must run against the server's database):

```sh
# Docker Compose server:
docker compose exec server trove-server agent create nas01
# bare-metal server: sudo TROVE_DB=/var/lib/trove/trove.db trove-server agent create nas01
```

## 2. Install the binary + unit

```sh
# grab the archive for your arch from the latest release. Set VERSION to the
# release you're installing (see https://github.com/techdox/trove/releases).
VERSION=0.12.4 # x-release-please-version
curl -fLO "https://github.com/techdox/trove/releases/download/v${VERSION}/trove-agent-local_${VERSION}_linux_amd64.tar.gz"
tar xzf trove-agent-local_${VERSION}_linux_amd64.tar.gz
sudo install -m 0755 trove-agent-local /usr/local/bin/

# the systemd unit is bundled in the archive under deploy/systemd/
sudo cp deploy/systemd/trove-agent-local.service /etc/systemd/system/

# configure — TROVE_SERVER_URL is your server's address as seen from THIS host
# (use http://localhost:8080 only if the server also runs on this box)
sudo tee /etc/trove-agent-local.env >/dev/null <<EOF
TROVE_SERVER_URL=http://YOUR-SERVER:8080
TROVE_TOKEN=trove_xxxxxxxx
EOF
sudo chmod 600 /etc/trove-agent-local.env

sudo systemctl enable --now trove-agent-local
journalctl -u trove-agent-local -f   # watch it connect
```

## Configuration

| Variable                  | Default      | Purpose                                                     |
| ------------------------- | ------------ | ------------------------------------------------------------ |
| `TROVE_SERVER_URL`        | _(required)_ | Base URL of the Trove server.                                |
| `TROVE_TOKEN`             | _(required)_ | Token from `agent create`.                                   |
| `TROVE_INTERVAL`          | `30s`        | Push interval.                                               |
| `TROVE_AGENT_NAME`        | OS hostname  | Becomes the reported **host name** shown on the dashboard for this box (unlike other agents, where this variable is informational only). |
| `TROVE_LOCAL_UNIT_FILTER` | _(all)_      | Glob to select units, e.g. `docker*` or `nginx.service`.     |
| `TROVE_LOCAL_ALL`         | `false`      | `true` to include inactive units (default: active + failed). |

## What you'll see

Each `.service` unit appears as a service with its systemd sub-state
(`running`, `exited`, `dead`, …). A `failed` unit shows as `unhealthy`;
everything else is `unknown` health (systemd has no app-level healthcheck) with
the state badge carrying the signal. By default only active/failed units are
reported to keep the noise down — set `TROVE_LOCAL_ALL=true` for everything.
