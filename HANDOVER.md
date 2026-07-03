# Handover — Phase 4 wrap-up

Written 2026-07-03 at the end of a session that ran low on usage mid-verification.
This file is temporary scaffolding for the next agent/session — delete it once
the remaining items below are done and pushed.

## State right now

- **Committed locally**, NOT pushed: commit `d26a3ce` on `main`, "Phase 4:
  configurable retention, unified events, alerts & email digest".
- Repo remote: `git@github.com:Techdox/trove.git` (private). Git identity for
  this repo is set locally (`user.name=techdox`, `user.email=nick@techdox.nz`,
  `core.sshCommand` pinned to `~/.ssh/id_rsa`) — see the `trove-constraints`
  memory file for why (global config would otherwise route through Nick's
  Portainer identity).
- Working tree is otherwise clean (`git status` should show nothing after the
  commit above, aside from anything this session's leftover E2E scratch files
  might have touched under `/private/tmp/...` — none of that is in the repo).
- **The user explicitly wants NO release/tag yet.** Public visibility flip and
  `v0.3.0` tagging wait until a final pre-launch review happens (see "What's
  next" below). Do not run `gh repo edit --visibility public` or push a tag
  without the user asking again in that later session.

## What Phase 4 shipped (all code-complete and committed)

1. **Retention (ROADMAP decision D1, now resolved)** — `internal/store/maintenance.go`
   (`Store.Prune`), `internal/server/maintenance.go` (hourly loop + startup
   run), wired in `cmd/trove-server/main.go`. Pruning was removed from the
   ingest write path in `internal/store/ingest.go`.
2. **Unified event model** — migration `internal/store/migrations/0004_event_model.sql`
   rebuilds `events` with a `kind` column (`state`/`health`/`agent`), nullable
   `service_id`/`agent_id`, and denormalized display fields; adds `meta` and
   `alert_state` tables; adds `agents.last_status`. `ApplyReport` (ingest.go)
   now also emits health-transition events; `Store.UpdateAgentStatus`
   (agents.go) emits agent heartbeat-transition events, called from the
   staleness loop in `internal/server/server.go`.
3. **Alert engine** — new package `internal/alert/`: `config.go` (env config),
   `engine.go` (cursor consumer, classify/deliver logic, cooldown/escalation/
   recovery, freshness sweep), `dispatch.go` (webhook/Discord/ntfy senders),
   `digest.go` + `smtp.go` (scheduled email digest). Wired into
   `cmd/trove-server/main.go` as two background goroutines. `trove-server
   alert test` subcommand added to the same file for channel verification.
4. **Docs** — `docs/alerts.md` (new), README and ROADMAP updated to reflect
   Phase 4 as delivered and D1 as resolved.

## What's verified (this session, all passed)

- `gofmt -l .` clean, `go vet ./...` clean, `go test ./...` green across all
  packages including new `internal/alert` tests (engine lifecycle, cooldown/
  escalation/recovery semantics, the `classify()` transition table, digest
  schedule parsing).
- **Migration upgrade test**: copied the live Phase-3 `trove.db` out of the
  running dev-stack container, opened it with the new binary (migration 0004
  applied cleanly), confirmed old events read back with `kind=state` and the
  API still serves them correctly.
- **`trove-server alert test`** exercised all four channels for real: a local
  Node.js HTTP sink for webhook + Discord payload shapes, a real round-trip
  to a random `ntfy.sh` topic (posted and read back via `/json?poll=1`), and
  a real SMTP send caught by a local Mailpit container.
- **Full live lifecycle E2E**: ran `trove-server` + `trove-agent-docker`
  against the real Docker daemon with the webhook sink live.
  - Stopping `trove-test-web` → `warning: ... stopped` + `critical: ...
    unhealthy`.
  - Starting it back up → both `resolved: ...` notices.
  - Killing the agent process → `warning: agent ... stale` then `critical:
    agent ... offline` (the offline alert correctly bypassed cooldown as an
    escalation).
  - Restarting the agent → `resolved: agent ... recovered`.
  - The initial ~40s "seed" window produced **zero** alerts, confirming a
    fresh install doesn't fire a false-alarm storm on first boot.

## What's NOT verified — the one open item

**The `docker-compose` dev-stack rebuild** (`docker compose up --build -d` in
the repo root) was interrupted mid-command by the user before it ran, so
Phase 4 has not been checked against a *rebuilt container image* — only
against locally-built Go binaries. This should be low-risk (the same code,
same tests, same manual E2E already passed against the raw binaries) but it's
the one box left unticked. To finish it:

```sh
cd /Users/nick/Library/CloudStorage/SynologyDrive-Active/Projects/Active/trove
docker compose up --build -d
curl -s --retry 20 --retry-connrefused --retry-delay 1 http://localhost:8080/healthz
# sanity checks:
curl -s http://localhost:8080/api/v1/services | jq '[.hosts[].services[]] | length'
curl -s http://localhost:8080/api/v1/agents
curl -s "http://localhost:8080/api/v1/events?limit=6" | jq -c '.events[] | {kind, service, agent, to_state}'
docker logs trove-server-1 2>&1 | grep -E "maintenance|alerting|digest|listening"
```

Confirm: services/agents populate as before, the events feed shows the new
`kind` field, and the log lines show the maintenance/alerting/digest loops
starting (they'll say "disabled" since no `TROVE_ALERT_*`/`TROVE_SMTP_*` env
is set in the dev compose file — that's expected and correct).

There were also some stray test containers on the Docker host from earlier
sessions (`trove-test-web`, `trove-test-ports`, `trove-test-longimg`,
`trove-mailpit`) — worth `docker rm -f`-ing them once this final check is
done if they're no longer wanted; they're not part of the repo.

## What's next (do NOT start without the user confirming)

Per the user's explicit instruction from this session: **after Phase 4 is
fully verified, the next step is one big final pre-launch planning session**
— a comprehensive review of the whole repo (code, docs, security, release
config) before flipping the GitHub repo to public and tagging `v0.3.0`. Do
not jump straight to tagging/releasing; that final review is a distinct,
deliberate step the user wants to do together.

The `melodic-herding-tower.md` plan file (if still present in
`~/.claude/plans/`) contains the Phase 4 plan that was executed here — useful
context but now historical; the final pre-launch review should be planned
fresh.

## Useful context / gotchas for the next session

- **Module path**: `github.com/techdox/trove` (renamed from bare `trove`
  during the OSS-readiness pass — this is intentional, not a mistake).
- **Version stamping**: all 5 binaries use `var version = "dev"` overridden
  via `-ldflags -X main.version=...`; the Makefile injects `git describe`.
  Don't reintroduce hardcoded version consts.
- **Read-only invariant is absolute**: no agent or server code path may ever
  mutate a monitored platform. This is stated as a hard rule in
  `CONTRIBUTING.md` — the alert engine only ever *sends* notifications
  outward, never touches monitored infra.
- A **memory file** exists at the project level
  (`~/.claude/projects/.../memory/trove-constraints.md`) recording the git
  identity setup and the read-only/OSS constraints — worth reading at the
  start of the next session.
