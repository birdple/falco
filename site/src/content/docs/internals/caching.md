---
title: Caching
description: What is cached, what is deliberately not, and why the hit ratio can be near zero without anything being wrong.
---

There is one cache in front of a transformation: an in-memory LRU, or Redis in
its place.

## The in-memory LRU

A sharded LRU in the process, capped by `CACHE_SIZE_MB` (256 by default) and
holding entries for `CACHE_TTL_HOURS` (24). `CACHE_CLEANUP_INTERVAL` decides how
often expired entries are swept — a different knob from the TTL, and confusing
the two has already made the TTL a no-op once.

`CACHE_SIZE_MB=0` disables it entirely, which is a reasonable setting behind a
CDN that already absorbs the repeat traffic. A single rendering larger than the
cache (or than one of its shards) is simply not cached, rather than flushing the
shard to make room.

**A restart loses all of it.** Nothing is lost permanently — the originals are in
storage — but the next request for each key pays a fetch plus a decode plus an
encode. On a busy deployment a redeploy at peak hour is visible in the latency
graph. A CDN in front is what makes that a non-event.

## Redis, optionally

`ENABLE_REDIS=true` with a `REDIS_URL` uses Redis **instead of** the in-memory
LRU: a cache that survives restarts and is shared between replicas. It is worth
it when you run more than one Falco, or when the working set is far larger than
what fits in memory. If Redis cannot be reached at startup, Falco falls back to
the LRU (or to no cache, with `CACHE_SIZE_MB=0`) and says so in the log; a
connection error later is logged and treated as a miss. A shutdown stops the
cache but never clears it, so other replicas sharing it stay warm.

## What is not cached

**Raw deliveries.** A request with no transformation, in the format the object
is already stored in, is streamed from storage and never stored in the cache.
There is no CPU to save, and caching it would evict transformations that did
cost something to produce. A format conversion (`/images/abc.avif` on a WebP
object) is CPU work, and is cached like a transformation.

This has a consequence worth knowing before reading a dashboard: a deployment
that mostly serves originals will show a cache hit ratio near zero, and that is
correct behaviour, not a misconfiguration.

## Cache keys

The key is computed from the request alone — the bucket the request resolves to,
the storage key, every parameter that changes the bytes, the format. That is
what allows the cache to be consulted *before* any storage I/O, and it is why the
delivery handler can decide which of its two branches to take without opening a
connection.

The bucket is part of it on purpose: `avatar` in bucket A and `avatar` in bucket
B are different objects, and a key without the bucket would serve one tenant's
variants to another. Floating-point parameters are written at full precision
(`rotate=45.4` and `rotate=45` are different keys), equivalent `pad_color`
spellings share one, and the watermark source is part of it by name.

Writing to a key drops every cached variant of it: an upload that overwrites an
existing key, an `update` and a `delete` all invalidate, so the next request
renders the new bytes. A render that raced such an invalidation is not cached.
`DELETE /api/v1/cache?key=…` does the same by hand, in the bucket `?b=` names or
the default one.

It also means that a client that varies a parameter per request — a cache-busting
timestamp, a slightly different width each time — gets a miss every time and
turns Falco into an encoder farm. A falling hit ratio with steady traffic is
usually exactly that, and the fix is on the client.

## Downstream

`Cache-Control` carries `max-age` and `s-maxage`, and deliberately **not**
`immutable`. An id is usually the hash of its content, but not always: a custom
id can be uploaded over, and `POST /api/v1/update` replaces the object behind a
key. `immutable` would stop browsers revalidating even on reload.

`update` drops the cached transformations of what it replaces, and cannot do
anything about the copies a CDN already holds — those live out their `s-maxage`. On a CDN-fronted deployment,
uploading under a new id and changing the reference is the predictable move.
