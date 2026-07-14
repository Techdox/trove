# Alerts & digest

Trove can push notifications the moment something changes — a host stops
reporting, a service goes unhealthy, a container dies — and send a scheduled
email digest summarizing the fleet. Everything is configured with environment
variables on **the server** (agents are not involved), and none of it changes
anything: notifications are outbound-only.

> **Where these variables go.** They must reach the *server process* — setting
> them in your shell with `export` does nothing. For a Docker Compose server,
> add them under the `server` service's `environment:` and re-run
> `docker compose up -d`. For a bare-metal server, put them in
> `/etc/trove-server.env` (the systemd unit reads it via `EnvironmentFile`) and
> `systemctl restart trove-server`.

Verify any setup with one command:

```sh
trove-server alert test
# compose: docker compose exec server trove-server alert test
```

It POSTs a test notification through every configured instant channel and, if
SMTP is set, sends a sample digest — printing one line per channel:

```
  webhook  ok
  discord  ok
  digest   ok
```

`ok` means the channel accepted the request (so also check that the message
actually arrived on your phone/Discord). If you see `no instant channels
configured`, the variables didn't reach the server — see the note above.

## Instant channels

Configure any combination; alerts fan out to all of them.

### Generic webhook

```sh
TROVE_ALERT_WEBHOOK_URL=https://n8n.lan/webhook/trove
```

POSTs JSON — the universal integration (n8n, Home Assistant, Gotify bridges,
custom scripts):

```json
{
  "kind": "health", "level": "critical",
  "title": "gitea unhealthy",
  "body": "gitea @ nuc01: health healthy → unhealthy",
  "host": "nuc01", "service": "gitea",
  "from": "healthy", "to": "unhealthy",
  "at": "2026-07-03T10:15:00Z"
}
```

`level` is one of `info`, `warning`, `critical`, `resolved`.

Optional request signing:

```sh
TROVE_ALERT_WEBHOOK_SECRET=change-me
```

When set, Trove adds:

- `X-Trove-Timestamp`: unix timestamp used in the signature payload
- `X-Trove-Signature`: `sha256=<hex hmac>`

The signature is HMAC-SHA256 over `timestamp + "." + raw_json_body` using the
secret. Receivers should verify the signature before trusting the payload, and
reject old timestamps if replay protection matters for their workflow.

### Discord

Server Settings → Integrations → Webhooks → New Webhook, copy the URL:

```sh
TROVE_ALERT_DISCORD_URL=https://discord.com/api/webhooks/…
```

Alerts arrive as color-coded embeds (red critical, yellow warning, green
resolved).

### ntfy

Push straight to your phone via [ntfy.sh](https://ntfy.sh) or a self-hosted
ntfy. Pick a hard-to-guess topic:

```sh
TROVE_ALERT_NTFY_URL=https://ntfy.sh/trove-a8f3k2
# self-hosted with auth:
TROVE_ALERT_NTFY_TOKEN=tk_…
```

Severity maps to ntfy priority (critical → urgent), so an offline host can
override quiet hours if you configure ntfy that way.

## What triggers an alert

| Kind        | Fires on                                                     | Level    |
| ----------- | ------------------------------------------------------------ | -------- |
| `agent`     | agent stale (missed 3 pushes) / offline (missed 10) / recovery | warning / critical / resolved |
| `host`      | host stale / offline / recovery while its agent is reporting | warning / critical / resolved |
| `health`    | service health → unhealthy, and recovery                     | critical / resolved |
| `state`     | service stopped/failed/removed/degraded, and recovery        | warning / resolved |
| `freshness` | a running image falls behind its registry tag                 | warning / resolved |

All five are on by default. Trim with:

```sh
TROVE_ALERT_EVENTS=agent,host,health     # e.g. quiet mode for chatty clusters
```

Built-in noise control (not configurable, by design):

- **Transitions only** — an ongoing bad state never re-alerts by itself.
- **New services don't alert on appearance** (feed-only) — a deploy isn't an
  incident. On Kubernetes, note that pod churn from deploys still produces
  `state: removed` alerts; set `TROVE_ALERT_EVENTS=agent,host,health` if that's
  too chatty for your cluster.
- **Cooldown** (`TROVE_ALERT_COOLDOWN`, default `5m`) — per service/agent/host,
  repeated flapping inside the window is suppressed, and a "resolved" notice
  is only sent for alerts that were actually delivered. Escalations (e.g.
  agent stale → offline) bypass the cooldown once.
- **No alarm floods at boot** — the engine starts from the current state; it
  never replays history or announces fifteen already-outdated images.
- When an agent goes offline, you get **one** agent alert, not one per
  service or host. If the agent recovers while one of its hosts remains
  missing, that host then alerts independently.

## Email digest

A scheduled summary — counts, unhealthy services, available updates, agents
not reporting, and the activity since the last digest.

```sh
TROVE_SMTP_HOST=smtp.fastmail.com
TROVE_SMTP_PORT=587              # 465 = implicit TLS, others use STARTTLS
TROVE_SMTP_USERNAME=nick@example.com
TROVE_SMTP_PASSWORD=app-password
TROVE_SMTP_FROM=trove@example.com
TROVE_SMTP_TO=nick@example.com   # comma-separated for multiple
TROVE_DIGEST=daily@08:00         # or weekly@mon:08:00, or off
```

`TROVE_SMTP_HOST`, `TROVE_SMTP_FROM`, and `TROVE_SMTP_TO` are the required trio
(username/password are optional, for open relays). If any of the three is
missing the digest silently stays off — `alert test` will report `email digest
not configured`.

Gmail: use an [app password](https://myaccount.google.com/apppasswords) with
`TROVE_SMTP_HOST=smtp.gmail.com`, port 587.

Times are the server's local time. If the server was down over a scheduled
slot, the digest is sent once on the next check (no double-sends).

## History retention

Alerting reads the same event stream the dashboard's activity feed shows.
Retention is configurable:

| Variable                  | Default | Purpose                                                   |
| ------------------------- | ------- | --------------------------------------------------------- |
| `TROVE_EVENT_RETENTION`   | `720h`  | How long state/health/agent events are kept.              |
| `TROVE_REMOVED_RETENTION` | `24h`   | How long removed services linger before purge.            |
| `TROVE_HOST_RETENTION`    | `720h`  | How long silent hosts and their inventory are retained.   |
