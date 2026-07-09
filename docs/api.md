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

By default, Trove returns all services grouped by agent and host for the dashboard.

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
- `kind`: exact event kind, for example `state`, `health`, `agent`, or `freshness`.
- `since`: Unix timestamp or RFC3339 time. Filters events by `at >= since`.

Example:

```sh
TROVE_API_TOKEN=TROVE_API_TOKEN_VALUE \
  curl --oauth2-bearer "$TROVE_API_TOKEN" \
  'https://trove.example/api/v1/events?kind=health&limit=50&offset=50'
```

## Metrics

`GET /metrics` exposes Prometheus text format and is protected like the other read APIs when OIDC/API-token auth is enabled.

Current metrics are intentionally non-sensitive:

- `trove_uptime_seconds`
- `trove_goroutines`
- `trove_alloc_bytes`
- `trove_sys_bytes`
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
