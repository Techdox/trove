# Trove scale baseline: 50 agents / 10,000 services / 100,000 events

Collected 2026-08-21 UTC against the unmodified v0.17.1 server binary (the
source tree is at the same release baseline). The run used the real HTTP report
endpoint and read APIs. No production data or deployment was touched.

## Reproduction

From the repository root:

```sh
# Either build the current source (requires Go), or point at a built binary.
go build -o /tmp/trove-server ./cmd/trove-server
TROVE_SERVER_BINARY=/tmp/trove-server python3 scripts/scale_benchmark.py
```

The harness creates exactly 50 agents through the production `agent create`
CLI, reports 200 services per agent, and sends 12 full snapshots. The initial
snapshot seeds state; 11 updates create 120,000 retained state-transition
events. It then measures paginated API reads, SQLite size/integrity, a restart,
and the event-delete portion of maintenance. It deliberately preserves
`Store.Open`'s intentional `SetMaxOpenConns(1)`.

## Baseline result

| Measure | Observed | Threshold | Result |
|---|---:|---:|---|
| Agents | 50 | >= 50 | PASS |
| Services | 10,000 | >= 10,000 | PASS |
| Retained events | 120,000 | >= 100,000 | PASS |
| Ingest requests | 600 | expected | PASS |
| Ingest latency p50 | 33.0 ms | <= 250 ms | PASS |
| Ingest latency p95 | 72.3 ms | <= 500 ms | PASS |
| Ingest latency max | 136.6 ms | <= 2,000 ms | PASS |
| `/api/v1/agents` p95 | 2.9 ms | <= 100 ms | PASS |
| `/api/v1/events?limit=500` p95 | 213.9 ms | <= 500 ms | PASS |
| `/api/v1/services?limit=500` p95 | 223.1 ms | <= 500 ms | PASS |
| Events response size | 100,620 bytes | <= 250,000 bytes | PASS |
| Services response size | 170,107 bytes | <= 250,000 bytes | PASS |
| SQLite main-file size | 21,049,344 bytes | <= 50 MiB | PASS |
| SQLite integrity | `ok` | `ok` | PASS |
| Restart to healthy `/healthz` | 104.5 ms | <= 2,000 ms | PASS |
| Maintenance event-delete phase | 643.4 ms | <= 5,000 ms | PASS |
| Server RSS at end | 23,304 KiB | <= 256 MiB | PASS |

Raw machine-readable output is retained in `benchmarks/scale-50x10k-baseline.json`.

## Profile findings

* The read APIs are the measured hot path: services p95 was 223.1 ms and events
  p95 was 213.9 ms, versus 2.9 ms for the agents list. This is expected to be
  dominated by 500-row SQL/DTO construction plus JSON serialization, and is
  still inside the 500 ms threshold.
* Ingest remained below 500 ms p95 with the single SQLite connection. The
  observed maximum was 136.6 ms in the final run, so this run provides no
  evidence that concurrent SQLite connections would help.
* SQLite grew to about 20.1 MiB for 10,000 services and 120,000 events. The
  event-delete maintenance phase completed in 643.4 ms, and restart completed
  in 104.5 ms. Neither is an optimization blocker at this scale.
* Integrity was `ok`; the benchmark did not change application code or schema.

## Decision and follow-up

Baseline thresholds pass. The API read path is the only measured relative
hotspot, but it is not currently a threshold violation. No optimization
follow-up card is justified by this baseline. Re-run this harness after any
pagination, query, serialization, or SQLite changes and compare the checked-in
JSON rather than assuming concurrency is beneficial.

## Limits

Maintenance timing measures the production event-delete predicate on a stopped
benchmark database. Production `Store.Prune` also handles removed services and
hosts; those branches are not populated by this scenario. CPU is recorded as
server `/proc` CPU ticks for the ingest window, while memory is the final RSS
sample, so these are directional process measurements rather than host-wide
resource accounting.
