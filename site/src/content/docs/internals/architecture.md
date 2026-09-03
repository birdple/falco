---
title: Architecture
description: One process, four layers, and the two decisions that shape everything else.
---

Falco is a single process with no database, no broker and no background workers.
Its whole durable state lives in the storage backend; everything else is an
in-memory cache that a restart is allowed to lose.

```
                    chi router
                        │
     ┌──────────────────┼──────────────────┐
     │                  │                  │
  upload            delivery             proxy
     │                  │                  │
     │            cache lookup        allowlist + SSRF
     │                  │                  │
     │             singleflight        HTTP fetch
     │                  │                  │
     └────────►  storage registry  ◄───────┘
                        │
        filesystem · S3 · R2 · Jay (+ backups)
                        │
                     libvips
```

## The upload path

1. The body is read under `MAX_FILE_SIZE_MB`.
2. The content type is **sniffed from the bytes**, not trusted from the header.
3. An image goes through libvips and is re-encoded; anything else is stored
   verbatim, except SVG, HTML and XML, which are refused.
4. The id is the hash of the raw content, so the same bytes always land on the
   same key. Backends are idempotent per key, which makes a repeated upload a
   no-op rather than a duplicate.
5. Metadata is written alongside, and backup targets are replicated according to
   their mode.

## The delivery path

Two branches, chosen entirely from the query string before any I/O:

**No transformation** — stream the stored object straight through. Nothing is
cached: there is no CPU work to amortise, and streaming keeps memory flat no
matter how large the file is.

**A transformation** — the cache key is computable from the query alone, so a hit
answers without touching storage at all. On a miss, a `singleflight` group makes
concurrent requests for the same key share one fetch, one decode and one encode
instead of each doing their own. That is what keeps a cold cache under a traffic
spike from turning into N simultaneous libvips decodes of the same image.

## Storage is a registry, not a backend

Every bucket in the configuration becomes an entry in a registry, and a request
picks one by name. Backups are a decorator over a bucket rather than a feature of
a backend, which is why "S3 backed up to R2" and "Jay backed up to the local
disk" are the same code.

Every backend call goes through a circuit breaker. When a backend starts failing,
the breaker opens and requests fail fast instead of piling up on timeouts and
exhausting connections.

## libvips, through cgo

The bindings are `github.com/cshum/vipsgen`, generated against libvips 8.18 —
not govips, which is the other common choice and a different API.

`vips.Startup` is given an explicit configuration rather than `nil`: vipsgen
reads a nil config as "SIMD off", which makes every encode measurably slower. It
is a one-line detail with a whole-service effect.

Concurrency into libvips is bounded by `CONCURRENT_WORKERS` through a semaphore.
Beyond it, requests queue rather than each taking a decode buffer, because
libvips memory is per in-flight operation and unbounded concurrency is how an
image service gets OOM-killed.

## What is deliberately absent

**No database.** Object metadata lives with the object in the backend. There is
nothing to migrate and nothing to keep consistent with a second store.

**No message broker.** Falco publishes no events and consumes none. In the
birdple stack every other service talks over NATS; Falco is called over HTTP and
answers over HTTP.

**No original.** Uploads are re-encoded and the source bytes are not kept. It
halves what a busy deployment stores, and it is a real constraint: if you need
the untouched file, Falco is not where it lives.
