---
title: Observability
description: Metrics, logs, traces and the one profile that exists for a specific known problem.
---

## Health

```bash
curl -s localhost:8080/health
```

```json
{"status":"healthy","version":"0.13.0","commit":"abc1234","uptime":"3h12m4s"}
```

No authentication — an orchestrator has to be able to poll it. The version and
commit are baked in at build time, so this is also the quickest way to confirm
which build is actually running. It answers `503` with `"status":"unhealthy"`
when the default backend does not respond.

## Metrics

`GET /metrics`, Prometheus format, mounted when `ENABLE_METRICS=true` **and**
`API_KEY_REQUIRED=true`, and guarded by the admin API key. Without an API key to
put it behind it is not mounted at all — Falco logs an error at startup instead
of serving it in the open.

Every metric is in the `falco_` namespace. What is worth alerting on:

| Metric | Reads as |
|---|---|
| `falco_http_requests_total`, `falco_http_request_duration_seconds` by method, route and status | The usual traffic picture |
| `falco_cache_hits_total`, `falco_cache_misses_total` | A hit ratio that falls off a cliff usually means a client started varying a parameter — a cache-busting query string, a new size per request |
| `falco_cache_size_bytes`, `falco_cache_item_count` | How close the LRU is to `CACHE_SIZE_MB`. Refreshed at most every 10 seconds, on cache writes |
| `falco_cache_evictions_total` | Rising steadily means the working set no longer fits and you are paying decode plus encode repeatedly |
| `falco_image_size_bytes`, `falco_image_processing_total` | What libvips is actually chewing through, and how often it fails |
| `falco_storage_circuit_breaker_open{bucket=…}` | `1` while that bucket's breaker is open and requests to it answer `503 STORAGE_UNAVAILABLE` |
| `falco_storage_replications_dropped_total` | Async backup replications dropped because 64 were already in flight. Any increase is a backup that is now stale |

The HTTP labels are bounded on purpose: the route label is the route pattern
(`/api/v1/images/*`), never the raw path, and a request no route matched is
labelled `unmatched`; a method outside the standard ones is labelled `OTHER`.
Otherwise every path a scanner probed would mint a series Prometheus keeps
forever.

Cache metrics only move for **rendered** responses — transformations, format
conversions and proxy fetches. A raw delivery streams straight from storage and
is never cached, so a deployment serving mostly originals will show a hit ratio
near zero without anything being wrong. See [Caching](/falco/internals/caching/).

## Readiness

`GET /health` is the liveness probe: it answers whether the process is up and
pings the **default** backend only.

`GET /health/ready` is the diagnostic one. It checks every registered backend,
reports each one's circuit-breaker state, and lists the features that are off
because they were never configured. Because that is a map of the deployment,
with raw backend errors, it sits behind the admin key:

```bash
curl -s -H "X-API-Key: $KEY" localhost:4009/health/ready | jq
```

```json
{
  "status": "ready",
  "version": "0.13.0",
  "uptime": "3h21m4s",
  "backends": [
    { "name": "jay", "type": "jay", "ok": true, "breaker": "closed" }
  ],
  "cache": { "enabled": true, "item_count": 812 },
  "disabled": [
    "watermark_url: WATERMARK_ALLOWED_HOSTS is unset (?wm_url= answers 403)"
  ]
}
```

It answers `503` with `"status": "degraded"` when any backend fails its check.
This is the endpoint to look at when uploads fail while delivery still works —
they are two different paths with different configuration.

## Without Prometheus

Three JSON endpoints cover the same ground for a quick look, behind the API key
(purging needs the admin key):

| Endpoint | Answers |
|---|---|
| `GET /api/v1/stats` | Objects and bytes per bucket. `free_space_bytes` is null unless the backend reports it |
| `GET /api/v1/cache` | Hit ratio, item count, size against `CACHE_SIZE_MB`, TTL and sweep interval |
| `DELETE /api/v1/cache` | Purges every variant, or one key's (`?key=`, in the bucket `?b=` names or the default one), and answers how many entries it dropped |

The admin panel's operations screen renders all of it, alongside the effective
configuration with secrets shown only as set or not set.

## Logs

Structured JSON through zerolog, `LOG_LEVEL` and `LOG_FORMAT` (`text` is for a
terminal, never for a deployment).

At startup Falco logs its resolved configuration — buckets, groups, port,
cache, processing and auth flags, never a credential. When a variable you
thought you set is not taking effect, that line is the place to look — it shows
what the process actually resolved, after all three layers. A variable that does
not parse never gets that far: the boot stops with an error naming it.

Error responses log the `error_code`, the status and the message, at a level that
follows the status: 5xx as error, 4xx as warning.

## Traces

OpenTelemetry over OTLP/gRPC, on when `OTEL_EXPORTER_OTLP_ENDPOINT` (or
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) is set. Every request gets a span with
method, route and status, and outbound fetches — proxy, watermark and
upload-by-URL — are child spans of the request that caused them.

Unset, telemetry is **off**: no exporter is created and nothing is pointed at a
default collector — otherwise local development drowns in "connection refused",
and every shutdown spends its budget trying to flush spans nobody receives. This
is the one place where an absent variable degrades quietly, and it is allowed
because nothing about correctness depends on it. On shutdown, telemetry is
flushed last, on its own 5-second budget.

## Profiles

`ENABLE_PPROF=true` mounts `/debug/pprof/*` behind the admin key: `heap`,
`goroutine`, `profile`, `trace`, `cmdline`, `symbol` — and `goroutineleak`.

That last one is there for a specific reason. Asynchronous backup replication
starts goroutines on `context.Background()`, detached from any request.
Shutdown drains them, but while the process runs nothing else waits on them, and
`goroutineleak` is what shows them when a deployment with `mode: async` backups
starts holding goroutines it never sheds.

Like `/metrics`, the profiles are only mounted with `API_KEY_REQUIRED=true`.

```bash
curl -H "X-API-Key: $KEY" localhost:8080/debug/pprof/goroutineleak -o leak.pprof
go tool pprof leak.pprof
```

The routes are mounted explicitly rather than by importing `net/http/pprof` for
its side effect: this router never falls through to `DefaultServeMux`, so the
blank import would register nothing reachable.
