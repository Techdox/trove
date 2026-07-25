# Upgrading Trove

Upgrades are meant to be boring. All state is one SQLite file and schema
migrations apply automatically on startup and are additive. For a rolling
upgrade, update the server first; agents from the immediately previous release
may lag while the rollout completes. Trove tests previous-release agent report
fixtures against the current server. Newer agents against an older server, or
larger version gaps, are not guaranteed. Pick the section that matches how you
run the server.

## Before you upgrade

- Skim the [release notes](https://github.com/techdox/trove/releases) /
  `CHANGELOG.md` for the version you're moving to.
- **Back up the database** (see [Backup](#backup)). Migrations only ever move
  forward, so a backup is your rollback path.
- In anything you care about, pin a specific version (e.g. `0.16.1`) <!-- x-release-please-version --> rather than
  `latest`, so upgrades are a deliberate change you control.

### Doctor preflight

Before upgrading—or when collecting a safe support report—run
`trove-server doctor` against the live server database. It checks database
access and integrity, migration state, local configuration, binary details, and
worker-relevant facts. The command is strictly read-only: it does not contact
external services, create a database, apply migrations, or print credentials.

```sh
# Docker Compose
docker compose exec server trove-server doctor

# Bare metal / systemd
sudo TROVE_DB=/var/lib/trove/trove.db trove-server doctor
```

Resolve any reported problem before proceeding with the upgrade.

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
(`ghcr.io/techdox/trove-server:0.16.1` <!-- x-release-please-version --> instead of `:latest`), then `pull` / `up -d`.

## Bare metal (systemd)

Download the release archive for the version you want and swap the binary in
place — the database at `/var/lib/trove/trove.db` is untouched:

```sh
VERSION=0.16.1 # x-release-please-version
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
go install github.com/techdox/trove/cmd/trove-server@v0.16.1   # x-release-please-version; or @latest
```

Then restart however you run it. (A `go install` build reports its version as
`dev` — that's expected and harmless; the real module version is still what you
installed.)

## Agents

Upgrade the server first, then upgrade agents. Agents from the immediately
previous release may continue reporting during the rollout; do not upgrade an
agent ahead of its server or assume compatibility across larger release gaps.

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
overwrite an existing destination file. It creates new parent directories as
`0700` and the backup itself as `0600`, independent of the caller's normal
umask. If you prefer SQLite's own CLI, this is
equivalent:

```sh
sqlite3 /var/lib/trove/trove.db ".backup '/var/backups/trove.db'"
```

### Verify a backup

Verify every new backup before relying on it. This command opens the backup
with SQLite read-only mode, runs `PRAGMA integrity_check`, reports its migration
record, and hashes the file before and after inspection. It never creates a
database, applies migrations, or changes the backup.

```sh
# Bare metal / a copied Compose backup on the host
trove-server backup verify /var/backups/trove/trove-20260725T021700Z.db

# Before copying a Compose backup out of the container
docker compose exec server trove-server backup verify /data/backups/trove-20260725T021700Z.db
```

`result: ok (backup opened and unchanged)` means that the file was readable and
internally consistent at verification time. It does not make an older server
binary compatible with a newer schema; follow the rollback procedure below.

### Scheduled backups and retention

Keep backups outside the live database volume and retain more than one recovery
point. The example below keeps 14 daily copies. Run the `find` command without
`-delete` once first to confirm exactly which files would age out; keep an
additional encrypted off-host copy for failures affecting the server itself.

Create a root-owned helper, changing the database and backup paths to match
your installation:

```sh
sudo install -d -o trove -g trove -m 0700 /var/backups/trove
sudo tee /usr/local/sbin/trove-backup >/dev/null <<'EOF'
#!/bin/sh
set -eu

backup_dir=/var/backups/trove
retention_days=14
backup="$backup_dir/trove-$(date -u +%Y%m%dT%H%M%SZ).db"

TROVE_DB=/var/lib/trove/trove.db /usr/local/bin/trove-server backup "$backup"
/usr/local/bin/trove-server backup verify "$backup"
find "$backup_dir" -maxdepth 1 -type f -name 'trove-*.db' -mtime +"$retention_days" -print -delete
EOF
sudo chown root:trove /usr/local/sbin/trove-backup
sudo chmod 0750 /usr/local/sbin/trove-backup
```

For cron, add this line to `/etc/cron.d/trove-backup` to run it daily at
02:17 UTC. The script verifies a new backup before pruning old ones.

```cron
17 2 * * * trove /usr/local/sbin/trove-backup
```

For systemd, use the same helper and install these units instead of cron:

```ini
# /etc/systemd/system/trove-backup.service
[Unit]
Description=Create and verify a Trove SQLite backup

[Service]
Type=oneshot
User=trove
Group=trove
ExecStart=/usr/local/sbin/trove-backup
```

```ini
# /etc/systemd/system/trove-backup.timer
[Unit]
Description=Run the Trove backup daily

[Timer]
OnCalendar=*-*-* 02:17:00 UTC
Persistent=true
RandomizedDelaySec=10m

[Install]
WantedBy=timers.target
```

Enable and inspect it with:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now trove-backup.timer
systemctl list-timers trove-backup.timer
```

### Restore rehearsal

Rehearse recovery before an upgrade and at least quarterly:

1. Create and verify a fresh backup; retain its SHA-256 output with the backup record.
2. Copy the backup to an isolated test path or host. Never start a server against the only backup copy.
3. Run `TROVE_DB=/srv/trove-rehearsal/trove.db trove-server doctor` against the copy.
4. Start the candidate server against that copy on loopback only, for example `TROVE_ADDR=127.0.0.1:18081 TROVE_DB=/srv/trove-rehearsal/trove.db trove-server`, then confirm `curl -fsS http://127.0.0.1:18081/healthz` returns `200`.
5. Check representative agents, hosts, services, and recent events in the test dashboard/API; stop the test server and keep the original backup unchanged.

Do not treat the database as disposable. In addition to current inventory and
event history, it contains agent token hashes, image-freshness cache state, and
alert cursor, cooldown, and per-channel delivery state. If the database is lost,
manually registered agents receive `401` responses because the new server no
longer recognises their tokens.

Restore a backup whenever possible. If no backup exists:

1. Recreate each production agent with `trove-server agent create <name>`.
2. Replace that agent's `TROVE_TOKEN` value (or Kubernetes Secret) with the newly
   issued token and restart the agent.
3. Wait for successful reports; current inventory repopulates after the agents
   authenticate and push again.

Historical events and previous alert cursor/delivery state cannot be rebuilt.
The quickstart's development-only bootstrap agent is recreated automatically
only when its existing `TROVE_BOOTSTRAP_AGENT` and `TROVE_BOOTSTRAP_TOKEN`
settings are still present.

## Rolling back

Migrations are forward-only; Trove never auto-downgrades the schema. To go back
to an older version after a newer one has run a migration:

1. Stop the server.
2. Restore the database backup you took **before** the upgrade.
3. Start the older binary / image.

If no new migration ran between the two versions, you can downgrade without
restoring. When unsure, restore the backup — an older binary may not understand
a newer schema.
