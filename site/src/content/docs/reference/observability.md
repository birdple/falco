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
