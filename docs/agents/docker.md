# Docker agent

Watches every container on a Docker host (running and stopped) and reports
name, image, state, health, ports, and compose labels. Strictly read-only —
the agent only issues `GET` requests to the Docker Engine API.

Run **one agent per Docker host**.

## 1. Mint a token

On the machine running `trove-server`:

```sh
trove-server agent create docker-nuc01
# compose install:
docker compose exec server trove-server agent create docker-nuc01
```

Copy the `trove_...` token — it is shown once.

## 2. Run the agent

```sh
docker run -d --name trove-agent --restart unless-stopped \
  -e TROVE_SERVER_URL=http://YOUR-SERVER:8080 \
  -e TROVE_TOKEN=trove_xxxxxxxx \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  ghcr.io/techdox/trove-agent-docker:latest
```

Replace `YOUR-SERVER` with your Trove server's address as reachable from this
host (a LAN IP or hostname; `localhost` only if the server runs on this same
box). The host and its containers appear on the dashboard within one push
interval (30s by default).

## Configuration

| Variable           | Default                       | Purpose                                            |
| ------------------ | ----------------------------- | -------------------------------------------------- |
| `TROVE_SERVER_URL` | _(required)_                  | Base URL of the Trove server.                      |
| `TROVE_TOKEN`      | _(required)_                  | Token from `agent create`.                         |
| `TROVE_INTERVAL`   | `30s`                         | Push interval (`30s`, `1m`, or bare seconds `30`). |
| `DOCKER_HOST`      | `unix:///var/run/docker.sock` | Docker endpoint (`unix://` or `tcp://`).           |

The name shown on the dashboard is the one you chose in `agent create`;
the reported hostname comes from the Docker daemon.

## Health mapping

- Container has a Docker healthcheck → its verdict is used (`healthy`/`unhealthy`).
- No healthcheck, running → `unknown` (the state badge carries the signal).
- Exited with restart policy `always`/`unless-stopped` → `unhealthy`
  (it was meant to stay up); otherwise `unknown`.

When a container is unhealthy, click its row: the detail drawer shows **why** —
the failing healthcheck's last output and exit code, or the exit code (and any
daemon error) of a container that stopped when it was meant to stay up.

## Notes

- Mounting the Docker socket is inherently sensitive. The agent's API usage is
  GET-only by construction — see `cmd/trove-agent-docker/docker.go`, which has
  no non-GET code path.
- Stopped containers are reported too, so things that *should* be running stay
  visible instead of vanishing.
