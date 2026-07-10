# Upgrading Trove

Upgrades are meant to be boring. All state is one SQLite file; schema migrations
apply automatically on startup and are additive; and the server and agents
tolerate version skew **within a minor version**, so you don't have to upgrade
everything in lockstep. Pick the section that matches how you run the server.

## Before you upgrade

- Skim the [release notes](https://github.com/techdox/trove/releases) /
  `CHANGELOG.md` for the version you're moving to.
- **Back up the database** (see [Backup](#backup)). Migrations only ever move
  forward, so a backup is your rollback path.
- In anything you care about, pin a specific version (e.g. `0.11.4`) <!-- x-release-please-version --> rather than
  `latest`, so upgrades are a deliberate change you control.

## Docker Compose

From the directory holding your compose file and `.env`:

```sh
docker compose pull
docker compose up -d
docker compose logs -f       # watch it come back
```

If you started from a named file, pass it: `docker compose -f docker-compose.server.yml pull`
then `... up -d`. The database lives in the `trove-data` volume and survives the
recreate; your `.env` (agent token) is reused.

**Pin a version:** set the image tag in the compose file
(`ghcr.io/techdox/trove-server:0.11.4` <!-- x-release-please-version --> instead of `:latest`), then `pull` / `up -d`.

## Bare metal (systemd)

Download the release archive for the version you want and swap the binary in
place — the database at `/var/lib/trove/trove.db` is untouched:

```sh
VERSION=0.11.4 # x-release-please-version
curl -fLO "https://github.com/techdox/trove/releases/download/v${VERSION}/trove-server_${VERSION}_linux_amd64.tar.gz"
tar xzf trove-server_${VERSION}_linux_amd64.tar.gz
sudo install -m 0755 trove-server /usr/local/bin/
sudo systemctl restart trove-server
```

Confirm it's healthy: `systemctl status trove-server` and
`journalctl -u trove-server -e`. The bare-metal agent (`trove-agent-local`)
upgrades the same way with its own archive, then `sudo systemctl restart trove-agent-local`.

## go install

```sh
go install github.com/techdox/trove/cmd/trove-server@v0.11.4   # x-release-please-version; or @latest
```

Then restart however you run it. (A `go install` build reports its version as
`dev` — that's expected and harmless; the real module version is still what you
installed.)

## Agents

Agents and the server tolerate version skew within a minor version, so upgrade
agents whenever convenient — they don't have to move in lockstep with the server.

- **Docker agent (compose):** `docker compose pull && docker compose up -d`.
- **Docker agent (`docker run`):** `docker pull ghcr.io/techdox/trove-agent-docker:latest`, then recreate the container.
- **Kubernetes agent:** `kubectl -n trove rollout restart deploy/trove-agent` (bump the image tag first if you pin one).
- **Bare-metal agent:** see [Bare metal](#bare-metal-systemd) above.

## Backup

Everything Trove knows is in one SQLite file:

- **Docker:** the `trove-data` volume → `/data/trove.db` in the container.
- **Bare metal:** `/var/lib/trove/trove.db`.

Use the built-in backup command for a consistent hot backup without stopping
the server:

```sh
# bare metal / systemd
sudo TROVE_DB=/var/lib/trove/trove.db trove-server backup "/var/backups/trove-$(date +%F).db"

# Docker Compose
mkdir -p ./backups
docker compose exec server trove-server backup /data/backups/trove-$(date +%F).db
docker compose cp server:/data/backups/trove-$(date +%F).db ./backups/
```

`trove-server backup` uses SQLite's online `VACUUM INTO` path and refuses to
overwrite an existing destination file. If you prefer SQLite's own CLI, this is
equivalent:

```sh
sqlite3 /var/lib/trove/trove.db ".backup '/var/backups/trove.db'"
```

Trove state is rebuildable anyway — agents repopulate the catalog within one
push interval, so a lost database costs you only event history.

## Rolling back

Migrations are forward-only; Trove never auto-downgrades the schema. To go back
to an older version after a newer one has run a migration:

1. Stop the server.
2. Restore the database backup you took **before** the upgrade.
3. Start the older binary / image.

If no new migration ran between the two versions, you can downgrade without
restoring. When unsure, restore the backup — an older binary may not understand
a newer schema.
