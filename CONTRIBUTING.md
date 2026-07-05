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

## Branching

`main` is protected — it takes PRs only, no direct pushes. Branch, commit,
push, open a PR:

```sh
git checkout -b fix/short-description   # or feat/..., docs/..., etc.
# ... commit ...
git push -u origin fix/short-description
gh pr create
```

CI (`gofmt`, `go vet`, `go test`, cross-compile) must pass on the PR before it
can merge; branches are deleted automatically after merging. There's no
persistent `dev` branch — branches are short-lived and scoped to one change,
so CI always runs against the real merge target.

## PRs

- Run `gofmt`, `go vet ./...`, and `go test ./...` before pushing — CI enforces
  all three.
- Use a roughly [Conventional Commits](https://www.conventionalcommits.org/)
  style commit message (`feat: ...`, `fix: ...`, `docs: ...`, `ci: ...`) —
  releases are versioned from these (see below).
- Keep schema changes additive and put them in a new numbered migration under
  `internal/store/migrations/`. Never edit an existing migration.
- New config should be env vars with safe defaults, documented in the README
  table.
- Small, reviewable PRs beat big ones. For anything architectural, open an
  issue first — the [ROADMAP](ROADMAP.md) explains what's deliberately
  deferred and why.

## Releases

[release-please](https://github.com/googleapis/release-please) watches every
merge to `main` and keeps a "chore(main): release X.Y.Z" PR open with the next
version number and changelog computed from commit messages since the last
release (`feat` → minor, `fix` → patch). When you want to ship, merge that
release PR.

Merging the release PR updates `CHANGELOG.md` and
`.release-please-manifest.json` on `main`. The
[release-tag.yml](.github/workflows/release-tag.yml) workflow sees that manifest
change, creates the matching `vX.Y.Z` tag, and that tag triggers
[release.yml](.github/workflows/release.yml) (goreleaser: cross-compiled
binaries + multi-arch Docker images to GHCR + a GitHub Release). So `main` is
always at most one release-PR merge away from being exactly what's published;
there's no separate manual tagging step.

release-please is configured with `skip-github-release: true` — it only manages
`CHANGELOG.md`, `.release-please-manifest.json`, and the version PR. The tagger
workflow creates the tag, and goreleaser remains the only thing that creates the
GitHub Release and its assets, so the tools never race to create the same
release.

Both release-please and release-tag use the fine-grained PAT in the
`RELEASE_PLEASE_TOKEN` repo secret, not the default `GITHUB_TOKEN`. GitHub won't
trigger downstream workflows (CI or tag-triggered releases) for refs created by
the built-in token, which would leave release automation stuck halfway through.
