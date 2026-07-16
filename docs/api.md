# API reference

Trove's API is read-mostly by design. Agents push full-state reports into the server, and humans or automation read catalog state back out.

If OIDC is enabled, read APIs require either an authenticated dashboard session or `Authorization: Bearer TROVE_API_TOKEN_VALUE` when `TROVE_API_TOKEN` is configured. Agent ingest always uses the agent token and is never gated by OIDC.

## Endpoints

| Method & path | Auth | Purpose |
| --- | --- | --- |
| `POST /api/v1/report` | Agent bearer token | Agent pushes a full-state report. |
| `GET /api/v1/services` | OIDC or optional API token | Services grouped by host. |
| `GET /api/v1/agents` | OIDC or optional API token | Agents with derived heartbeat status. |
| `GET /api/v1/events` | OIDC or optional API token | Recent state-change events. |
| `GET /api/v1/me` | OIDC or optional API token | Current dashboard/API auth state. |
| `GET /metrics` | OIDC or optional API token | Prometheus text metrics. |
| `GET /healthz` | none | Liveness and database reachability. |

## Pagination and filtering

### `GET /api/v1/services`

By default, Trove returns all services grouped by agent and host for the dashboard. Reported hosts with no services are included with an empty `services` array so their liveness remains visible.

Each host group includes its own derived `status` (`ok`, `stale`, `offline`, or
`unknown`) and `last_seen_at`, plus `agent_status` for the owning agent. Host
and agent status can differ when a multi-host agent continues reporting some
hosts after another disappears.

`condition` is the platform's latest verdict (`normal`, `warning`, `critical`,
or `unknown`). It is deliberately separate from reporting `status`: for
example, a Proxmox agent can keep reporting normally while one cluster node is
offline and therefore `critical`.

`metrics` is the latest point-in-time resource snapshot reported for the host.
Fields the platform cannot provide are omitted; an empty object means no host
metrics are currently available. Trove stores only the latest snapshot and
does not turn a brief resource spike into a health verdict.

```json
{
  "agent": "proxmox",
  "hostname": "pve-a",
  "platform": "proxmox",
  "status": "ok",
  "condition": "normal",
  "metrics": {
    "cpu_usage_ratio": 0.125,
    "cpu_logical_count": 16,
    "load_1": 0.5,
    "load_5": 0.25,
    "load_15": 0.125,
    "memory": { "used_bytes": 8589934592, "total_bytes": 34359738368 },
    "root_disk": { "used_bytes": 42949672960, "total_bytes": 107374182400 },
    "uptime_seconds": 90061
  }
}
```

Optional query parameters:

- `limit`: positive integer, capped at `500`.
- `offset`: non-negative integer. If `offset` is set without `limit`, Trove uses `500`.
- `since`: Unix timestamp or RFC3339 time. Filters services by `updated_at >= since`.

Example:

```sh
TROVE_API_TOKEN=TROVE_API_TOKEN_VALUE \
  curl --oauth2-bearer "$TROVE_API_TOKEN" \
  'https://trove.example/api/v1/services?limit=100&offset=0&since=2026-07-01T00:00:00Z'
```

When pagination or filtering is used, the response includes:

```json
{
  "pagination": {
    "limit": 100,
    "offset": 0,
    "count": 100,
    "next_offset": 100
  }
}
```

`next_offset` appears when Trove returned a full page.

### `GET /api/v1/events`

Events default to the newest 100 rows.

Optional query parameters:

- `limit`: positive integer, capped at `500`.
- `offset`: non-negative integer.
- `kind`: exact event kind, for example `state`, `health`, `agent`, `host`, or `freshness`.
- `since`: Unix timestamp or RFC3339 time. Filters events by `at >= since`.

Service events include `service_id`; host events include `host_id`. These are
soft historical references and remain in the feed after their subject is pruned.

Example:

```sh
TROVE_API_TOKEN=TROVE_API_TOKEN_VALUE \
  curl --oauth2-bearer "$TROVE_API_TOKEN" \
  'https://trove.example/api/v1/events?kind=health&limit=50&offset=50'
```

## Prometheus metrics

`GET /metrics` exposes Prometheus text format and is protected like the other read APIs when OIDC/API-token auth is enabled.

Current metrics are intentionally non-sensitive:

- `trove_uptime_seconds`
- `trove_reports_accepted_total`
- `trove_sqlite_database_size_bytes`
- `trove_agents{status=...}`
- `trove_services_by_health{health=...}`
- `trove_services_by_state{state=...}`
- `trove_services_by_kind{kind=...}`
- `trove_events{kind=...}`
- `trove_service_image_freshness{status=...}`

Scrape example:

```yaml
scrape_configs:
  - job_name: trove
    metrics_path: /metrics
    bearer_token: ${TROVE_API_TOKEN}
    static_configs:
      - targets: ['trove.example']
```

## Webhook signatures

Generic alert webhooks can be signed with `TROVE_ALERT_WEBHOOK_SECRET`. See [alerts.md](alerts.md) for the signature headers and verification contract.
