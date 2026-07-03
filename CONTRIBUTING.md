# Contributing to Trove

Thanks for wanting to help! Trove is a small, focused project and aims to stay
that way. This page covers the ground rules and how to get a dev environment
running in a couple of minutes.

## The one hard rule: read-only

Trove observes infrastructure; it never changes it. **No contribution may add a
code path that mutates a monitored platform** — no restart buttons, no deploy
hooks, no exec, no "just this one convenience write." Agents talk to their
platforms exclusively through read/list calls (the Docker agent literally only
issues `GET`s). PRs that break this invariant will be declined regardless of
how useful the feature is; it is the project's core design decision, not a
missing feature.

## Dev environment

Requirements: Go 1.26+, Docker with Compose, `make`.

```sh
git clone https://github.com/techdox/trove.git
cd trove

make native        # build all binaries into bin/ for your host platform
make test          # go test ./...
make vet           # go vet
docker compose up --build   # dev stack: server + docker agent watching your machine
```

The dev stack (root `docker-compose.yml`) auto-seeds an agent token via
`TROVE_BOOTSTRAP_*` so it works with zero setup. Open <http://localhost:8080>.

The dashboard is vanilla JS/CSS embedded via `embed.FS` — there is no frontend
build step. Edit files under `web/public/` and rebuild the server binary (or
`docker compose up --build server`) to see changes.

## Layout, briefly

```
cmd/                one main per binary (server + one agent per platform)
internal/agentkit/  shared agent config/push/loop — new agents implement
                    the Collector interface and call agentkit.Run
internal/store/     all SQL lives here (SQLite, embedded migrations)
internal/server/    HTTP handlers, auth, background tickers
pkg/model/          the agent<->server wire contract (keep it lean;
                    agents import only this)
web/public/         dashboard SPA (no framework, no build step)
```

Adding an agent for a new platform is deliberately cheap: implement
`agentkit.Collector`, map your platform's objects onto `model.ReportService`,
and reuse everything else. See `cmd/trove-agent-proxmox` for a compact example.

## PRs

- Run `gofmt`, `go vet ./...`, and `go test ./...` before pushing — CI enforces
  all three.
- Keep schema changes additive and put them in a new numbered migration under
  `internal/store/migrations/`. Never edit an existing migration.
- New config should be env vars with safe defaults, documented in the README
  table.
- Small, reviewable PRs beat big ones. For anything architectural, open an
  issue first — the [ROADMAP](ROADMAP.md) explains what's deliberately
  deferred and why.
