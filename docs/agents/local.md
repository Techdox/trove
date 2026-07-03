# Bare-metal (systemd) agent

Watches a plain Linux host's systemd service units — the box that isn't
running Docker or Kubernetes but still matters. Read-only: it only ever runs
`systemctl list-units` (a query command).

Runs directly on the host (not in a container), since it needs the host's
systemd. Ships as a static binary in the release archives.

## 1. Mint a token

```sh
trove-server agent create nas01
```

## 2. Install the binary + unit

```sh
# grab the archive for your arch from the latest release
curl -LO https://github.com/techdox/trove/releases/latest/download/trove-agent-local_<version>_linux_amd64.tar.gz
tar xzf trove-agent-local_*_linux_amd64.tar.gz
sudo install -m 0755 trove-agent-local /usr/local/bin/

sudo cp deploy/systemd/trove-agent-local.service /etc/systemd/system/ 2>/dev/null \
  || sudo cp trove-agent-local.service /etc/systemd/system/   # unit is bundled in the archive

# configure
sudo tee /etc/trove-agent-local.env >/dev/null <<EOF
TROVE_SERVER_URL=http://trove.lan:8080
TROVE_TOKEN=trove_xxxxxxxx
EOF
sudo chmod 600 /etc/trove-agent-local.env

sudo systemctl enable --now trove-agent-local
```

## Configuration

| Variable                  | Default      | Purpose                                                     |
| ------------------------- | ------------ | ------------------------------------------------------------ |
| `TROVE_SERVER_URL`        | _(required)_ | Base URL of the Trove server.                                |
| `TROVE_TOKEN`             | _(required)_ | Token from `agent create`.                                   |
| `TROVE_INTERVAL`          | `30s`        | Push interval.                                               |
| `TROVE_LOCAL_UNIT_FILTER` | _(all)_      | Glob to select units, e.g. `docker*` or `nginx.service`.     |
| `TROVE_LOCAL_ALL`         | `false`      | `true` to include inactive units (default: active + failed). |

## What you'll see

Each `.service` unit appears as a service with its systemd sub-state
(`running`, `exited`, `dead`, …). A `failed` unit shows as `unhealthy`;
everything else is `unknown` health (systemd has no app-level healthcheck) with
the state badge carrying the signal. By default only active/failed units are
reported to keep the noise down — set `TROVE_LOCAL_ALL=true` for everything.
