# Alerts & digest

Trove can push notifications the moment something changes — a host stops
reporting, a service goes unhealthy, a container dies — and send a scheduled
email digest summarizing the fleet. Everything is configured with environment
variables on **the server** (agents are not involved), and none of it changes
anything: notifications are outbound-only.

Verify any setup with one command:

```sh
trove-server alert test
# compose: docker compose exec server trove-server alert test
```

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
| `health`    | service health → unhealthy, and recovery                     | critical / resolved |
| `state`     | service stopped/failed/removed/degraded, and recovery        | warning / resolved |
| `freshness` | a running image falls behind its registry tag                 | warning / resolved |

All four are on by default. Trim with:

```sh
TROVE_ALERT_EVENTS=agent,health     # e.g. quiet mode for chatty clusters
```

Built-in noise control (not configurable, by design):

- **Transitions only** — an ongoing bad state never re-alerts by itself.
- **New services don't alert on appearance** (feed-only) — a deploy isn't an
  incident. On Kubernetes, note that pod churn from deploys still produces
  `state: removed` alerts; set `TROVE_ALERT_EVENTS=agent,health` if that's
  too chatty for your cluster.
- **Cooldown** (`TROVE_ALERT_COOLDOWN`, default `5m`) — per service/agent,
  repeated flapping inside the window is suppressed, and a "resolved" notice
  is only sent for alerts that were actually delivered. Escalations (e.g.
  agent stale → offline) bypass the cooldown once.
- **No alarm floods at boot** — the engine starts from the current state; it
  never replays history or announces fifteen already-outdated images.
- When an agent goes offline, you get **one** agent alert, not one per
  service on that host.

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

Gmail: use an [app password](https://myaccount.google.com/apppasswords) with
`TROVE_SMTP_HOST=smtp.gmail.com`, port 587.

Times are the server's local time. If the server was down over a scheduled
slot, the digest is sent once on the next check (no double-sends).

## History retention

Alerting reads the same event stream the dashboard's activity feed shows.
Retention is configurable:

| Variable                  | Default | Purpose                                        |
| ------------------------- | ------- | ---------------------------------------------- |
| `TROVE_EVENT_RETENTION`   | `720h`  | How long state/health/agent events are kept.   |
| `TROVE_REMOVED_RETENTION` | `24h`   | How long removed services linger before purge. |
