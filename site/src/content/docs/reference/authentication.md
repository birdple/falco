---
title: Authentication
description: One admin key, any number of scoped keys, and a signature on every URL a browser fetches.
---

Falco has two separate questions to answer: *may this caller write?* and *may
this caller read this image?* They have different answers because they have
different clients — an API key belongs in a server-to-server call, and a browser
rendering an `<img>` cannot send one.

## Writes: API keys

With `API_KEY_REQUIRED=true`, `upload`, `update`, `list`, `delete`, `sign`,
`meta`, `stats` and `cache` require a key, in either header:

```
X-API-Key: sk-…
Authorization: Bearer sk-…
```

A missing or wrong key is `401 UNAUTHORIZED`.

`API_KEY` is the **admin key**: unrestricted, every bucket. Everything else is a
scoped key declared on a bucket, a group or a subgroup — see
[Buckets and groups](/falco/guides/buckets/).

A request whose key does not cover the bucket it names gets `403 ACCESS_DENIED`.
That check runs on upload, update, list, delete, metadata **and** signing.
Signing is the one people forget: without it, a key scoped to bucket A could
mint a signed URL for bucket B and read it at delivery time, where no key is
checked at all. `/sign` resolves the path it is asked to sign exactly the way
delivery will — `?b=`, `?bucket=` **and** `?storage=` — so none of them can
smuggle another bucket into a signed URL.

A request that names **no** bucket is checked against the default one. Leaving
`?b=` out is not a way around a scope: a key scoped to bucket B cannot upload,
list or delete in the default bucket by omitting it.

A scoped key whose bucket set resolves to nothing grants nothing — and the
startup validation refuses such a key rather than letting it boot. Purging the
cache (`DELETE /api/v1/cache`) needs the admin key; `/api/v1/stats` only lists
the buckets the key can reach.

When no scoped keys are configured, the admin key is the only key and the scope
machinery stays out of the way. That is how birdple runs it.

Keys are compared in constant time. Falco logs a warning at startup for any key
shorter than 24 characters; it still boots, so an existing deployment keeps
running while its keys are rotated.

## Reads: public by signature

**`HMAC_REQUIRED=true`.** Delivery (`/api/v1/images/*`) and the proxy
(`/api/v1/proxy/*`) take no API key. They require a valid `sig` instead, and the
signature covers path and query, so the only transformations that exist are the
ones you minted. This is the regime to deploy. See
[Signed URLs](/falco/guides/signed-urls/).

**`HMAC_REQUIRED=false`.** Delivery and the proxy are **open**. There is no
"API key instead" mode for them — the startup validation only allows this
setting together with `API_KEY_REQUIRED=false`, so it is the shape of a local or
development deployment with auth switched off altogether.

## The combinations Falco refuses

```
API_KEY_REQUIRED=true
HMAC_REQUIRED=false
```

This does not start. It looks like the careful choice — protect the writes,
leave reads public — and it is the one that leaves `/api/v1/images/*` reachable
by anyone who can guess an id, with every transformation parameter available to
burn your CPU. If you want writes protected, delivery gets signed too.

```
API_KEY_REQUIRED=false
HMAC_REQUIRED=true
```

This does not start either. `/api/v1/sign` sits behind the API key, so with the
key off anyone could mint a valid signature and the signature on delivery would
protect nothing.

Startup also refuses `HMAC_REQUIRED=true` without `HMAC_KEY` and `HMAC_SALT`,
an `HMAC_KEY` or `HMAC_SALT` that is not hex, and an `HMAC_SIGNATURE_SIZE` that
is neither `0` (the full 32 bytes) nor between 16 and 32.

## What is not authenticated

- `/health` — the orchestrator polls it, and it reveals status, version, commit
  and uptime only.
- `/robots.txt`, `/docs`, `/docs/openapi.yaml`.
- The admin panel's login page. The panel itself authenticates internally, with
  the API key, and holds a session cookie afterwards.

`/health/ready`, `/metrics` and `/debug/pprof/*` are **not** in that list: they
sit behind the admin key (scoped keys do not open them). `/health/ready` maps
every backend and returns their
raw errors, a heap profile exposes internal process state, and metrics expose
traffic shape. `/metrics` and `/debug/pprof/*` are not mounted at all unless
`API_KEY_REQUIRED=true` — setting `ENABLE_METRICS` or `ENABLE_PPROF` without it
logs an error instead of serving them in the open. With `API_KEY_REQUIRED=false`
every other route, `/health/ready` included, is as open as the API itself.

## Trusted proxies

Rate limiting is per client, and the client IP comes from the socket unless
`TRUSTED_PROXIES` says otherwise. Empty means loopback only: `X-Forwarded-For`
from anywhere else is ignored.

That is fail-closed on purpose — believing the header by default lets anyone
send `X-Forwarded-For: <random>` and get a fresh rate limit bucket per request.
Behind a proxy, list its subnet, or every request will be counted against the
proxy instead of the caller.

When the peer is trusted, the client is the **rightmost** `X-Forwarded-For`
entry that is not itself a trusted proxy. The leftmost entry is whatever the
client chose to write, and every common proxy appends to the header rather than
replacing it.

## Rate limiting

`RATE_LIMIT_RPM` (1000 by default, `0` disables it) is a token bucket per
client: a client may spend `RATE_LIMIT_RPM` plus a burst of 100 at once, then
refills at `RATE_LIMIT_RPM` per minute. IPv6 clients are keyed by their `/64`,
since a single host routinely holds a whole one. Over the limit is
`429 RATE_LIMITED` with `Retry-After`.
