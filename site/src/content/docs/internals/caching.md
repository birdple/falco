---
title: Caching
description: What is cached, what is deliberately not, and why the hit ratio can be near zero without anything being wrong.
---

There are two caches in front of a transformation and one is optional.

## The in-memory LRU

A sharded LRU in the process, capped by `CACHE_SIZE_MB` (256 by default) and
holding entries for `CACHE_TTL_HOURS` (24). `CACHE_CLEANUP_INTERVAL` decides how
often expired entries are swept — a different knob from the TTL, and confusing
the two has already made the TTL a no-op once.

`CACHE_SIZE_MB=0` disables it entirely, which is a reasonable setting behind a
CDN that already absorbs the repeat traffic.

**A restart loses all of it.** Nothing is lost permanently — the originals are in
storage — but the next request for each key pays a fetch plus a decode plus an
encode. On a busy deployment a redeploy at peak hour is visible in the latency
graph. A CDN in front is what makes that a non-event.

## Redis, optionally

`ENABLE_REDIS=true` with a `REDIS_URL` adds a second layer that survives
restarts and is shared between replicas. It is worth it when you run more than
one Falco, or when the working set is far larger than what fits in memory.

## What is not cached

**Raw deliveries.** A request with no transformation is streamed from storage and
never stored in the cache. There is no CPU to save, and caching it would evict
transformations that did cost something to produce.

This has a consequence worth knowing before reading a dashboard: a deployment
that mostly serves originals will show a cache hit ratio near zero, and that is
correct behaviour, not a misconfiguration.

## Cache keys

The key is computed from the query string alone — the id, the parameters, the
format. That is what allows the cache to be consulted *before* any storage I/O,
and it is why the delivery handler can decide which of its two branches to take
without opening a connection.

It also means that a client that varies a parameter per request — a cache-busting
timestamp, a slightly different width each time — gets a miss every time and
turns Falco into an encoder farm. A falling hit ratio with steady traffic is
usually exactly that, and the fix is on the client.

## Downstream

`Cache-Control` carries `max-age`, `s-maxage` and `immutable`. The `immutable` is
truthful because an id is the hash of its content: a given URL cannot ever
describe different bytes.

`POST /api/v1/update` is the exception. It replaces the object behind an id, drops
the cached transformations of it, and cannot do anything about the copies a CDN
already holds — those live out their `s-maxage`. On a CDN-fronted deployment,
uploading under a new id and changing the reference is the predictable move.
