---
title: Observability
description: Metrics, logs, traces and the one profile that exists for a specific known problem.
---

## Health

```bash
curl -s localhost:8080/health
```

```json
{"status":"healthy","version":"0.13.0","uptime":"3h12m4s"}
```

No authentication — an orchestrator has to be able to poll it. The version is
baked in at build time, so this is also the quickest way to confirm which build
is actually running.

## Metrics

`GET /metrics`, Prometheus format, mounted when `ENABLE_METRICS=true` and guarded
by the admin API key.

What is worth alerting on:

| Metric | Reads as |
|---|---|
| Request count and latency, by route and status | The usual traffic picture |
| Cache hits and misses | A hit ratio that falls off a cliff usually means a client started varying a parameter — a cache-busting query string, a new size per request |
| Cache size and item count | How close the LRU is to `CACHE_SIZE_MB` |
| Evictions | Rising steadily means the working set no longer fits and you are paying decode plus encode repeatedly |
| Processed bytes | What libvips is actually chewing through |

Cache metrics only move for **transformed** responses. A raw delivery streams
straight from storage and is never cached, so a deployment serving mostly
originals will show a hit ratio near zero without anything being wrong. See
[Caching](/falco/internals/caching/).

## Readiness

`GET /health` is the liveness probe: it answers whether the process is up and
pings the **default** backend only.

`GET /health/ready` is the diagnostic one. It checks every registered backend,
reports each one's circuit-breaker state, and lists the features that are off
because they were never configured:

```bash
curl -s localhost:4009/health/ready | jq
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

Three JSON endpoints cover the same ground for a quick look, behind the API key:

| Endpoint | Answers |
|---|---|
| `GET /api/v1/stats` | Objects and bytes per bucket. `free_space_bytes` is null unless the backend reports it |
| `GET /api/v1/cache` | Hit ratio, item count, size against `CACHE_SIZE_MB`, TTL and sweep interval |
| `DELETE /api/v1/cache` | Purges every variant, or one key's, and answers how many entries it dropped |

The admin panel's operations screen renders all of it, alongside the effective
configuration with secrets shown only as set or not set.

## Logs

Structured JSON through zerolog, `LOG_LEVEL` and `LOG_FORMAT` (`text` is for a
terminal, never for a deployment).

At startup Falco dumps its whole resolved configuration with secrets redacted.
When a variable you thought you set is not taking effect, that dump is the place
to look — it shows what the process actually resolved, after all three layers.

Error responses log the `error_code`, the status and the message, at a level that
follows the status: 5xx as error, 4xx as warning.

## Traces

OpenTelemetry over OTLP, on when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Every
request gets a span with method, route and status.

Unset, telemetry initialisation is **skipped, not failed** — otherwise local
development drowns in "connection refused" for a collector nobody is running.
This is the one place where an absent variable degrades quietly, and it is
allowed because nothing about correctness depends on it.

## Profiles

`ENABLE_PPROF=true` mounts `/debug/pprof/*` behind the admin key: `heap`,
`goroutine`, `profile`, `trace`, `cmdline`, `symbol` — and `goroutineleak`.

That last one is there for a specific reason. Asynchronous backup replication
starts goroutines on `context.Background()` which no `WaitGroup` awaits and
shutdown does not consider. `goroutineleak` is what shows them when a deployment
with `mode: async` backups starts growing goroutines it never sheds.

```bash
curl -H "X-API-Key: $KEY" localhost:8080/debug/pprof/goroutineleak -o leak.pprof
go tool pprof leak.pprof
```

The routes are mounted explicitly rather than by importing `net/http/pprof` for
its side effect: this router never falls through to `DefaultServeMux`, so the
blank import would register nothing reachable.
