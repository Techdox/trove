# Rotating an agent token

Rotate an agent token when a host changes hands, a token may have been exposed,
or as part of routine credential maintenance. Rotation preserves the agent, its
hosts, services, events, and alert state.

The replacement is intentionally immediate: when `agent rotate` succeeds, the
old token is invalid. Schedule a short maintenance window and have the agent's
configuration ready to update before running it. A temporary `401` and a stale
agent while the old process is still running are expected; they stop once the
agent is recreated or restarted with the replacement token.

## 1. Mint the replacement token

Run this on the machine that owns the Trove database:

```sh
# Docker Compose server:
docker compose exec server trove-server agent rotate <name>

# Bare-metal server:
sudo TROVE_DB=/var/lib/trove/trove.db trove-server agent rotate <name>
```

The replacement `trove_...` token is shown once. Store it in the platform's
secret or environment file immediately. Do not put it in source control, shell
history, or a ticket.

## 2. Replace the token and restart the agent

### Docker Compose

Update `TROVE_TOKEN` in the Compose `.env` file, then recreate **only** the
agent service. The server does not need a restart:

```sh
docker compose up -d --force-recreate agent
```

If you use the Proxmox Compose example, include its file name:

```sh
docker compose -f docker-compose.proxmox.yml up -d --force-recreate agent
```

### Docker run

Container environment variables cannot be changed in place. Remove and recreate
the agent container with the same command used at installation, substituting the
replacement `TROVE_TOKEN`:

```sh
docker rm -f trove-agent
# Re-run the documented docker run command with the replacement TROVE_TOKEN.
```

### Kubernetes

Replace the Secret value, then restart the Deployment so it reads the new
environment variable:

```sh
read -r -s TROVE_REPLACEMENT
printf '\n'
kubectl -n trove create secret generic trove-agent \
  --from-literal=token="$TROVE_REPLACEMENT" \
  --dry-run=client -o yaml | kubectl apply -f -
unset TROVE_REPLACEMENT
kubectl -n trove rollout restart deployment/trove-agent
kubectl -n trove rollout status deployment/trove-agent
```

### Bare-metal Linux (systemd)

Edit the private environment file, replace only `TROVE_TOKEN`, then restart the
unit:

```sh
sudoedit /etc/trove-agent-local.env
sudo systemctl restart trove-agent-local
sudo systemctl status trove-agent-local --no-pager
```

## 3. Verify

Check the platform's logs for a successful report and confirm the agent returns
to `reporting ok` in Trove. If it keeps returning `401`, the replacement was
saved to the wrong config file, the old process was not recreated, or the token
was minted against a different Trove database.

Never use `agent create` as a rotation workaround: it creates a second agent
and splits the inventory/history. `agent rotate <name>` keeps the existing
agent identity intact.
