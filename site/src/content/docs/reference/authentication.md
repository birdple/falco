---
title: Authentication
description: One admin key, any number of scoped keys, and two regimes for the delivery route.
---

Falco has two separate questions to answer: *may this caller write?* and *may
this caller read this image?* They have different answers because they have
different clients — an API key belongs in a server-to-server call, and a browser
rendering an `<img>` cannot send one.

## Writes: API keys

With `API_KEY_REQUIRED=true`, `upload`, `update`, `list`, `delete` and `sign`
require a key, in either header:

```
X-API-Key: sk-…
Authorization: Bearer sk-…
```

`API_KEY` is the **admin key**: unrestricted, every bucket. Everything else is a
scoped key declared on a bucket, a group or a subgroup — see
[Buckets and groups](/falco/guides/buckets/).

A request whose key does not cover the bucket it names gets `403 ACCESS_DENIED`.
That check runs on upload, list, delete, delivery **and** signing. Signing is the
one people forget: without it, a key scoped to bucket A could mint a signed URL
for bucket B and read it at delivery time, where no key is checked at all.

When no scoped keys are configured, the admin key is the only key and the scope
machinery stays out of the way. That is how birdple runs it.

## Reads: two regimes

**`HMAC_REQUIRED=true` — public by signature.** Delivery takes no API key. The
signature covers path and query, so the only transformations that exist are the
ones you minted. This is the regime to deploy. See
[Signed URLs](/falco/guides/signed-urls/).

**`HMAC_REQUIRED=false` — key plus scope.** Delivery falls back to API key
authentication, honouring scopes, so a misconfigured deployment is not wide
open. Usable for a private service; not usable from a browser.

## The combination Falco refuses

```
API_KEY_REQUIRED=true
HMAC_REQUIRED=false
```

This does not start. It looks like the careful choice — protect the writes,
leave reads public — and it is the one that leaves `/api/v1/images/*` reachable
by anyone who can guess an id, with every transformation parameter available to
burn your CPU. If you want writes protected, delivery gets signed too.

## What is not authenticated

- `/health` — the orchestrator polls it, and it reveals status, version and
  uptime only.
- `/robots.txt`, `/docs`, `/docs/openapi.yaml`.
- The admin panel's login page. The panel itself authenticates internally, with
  the API key, and holds a session cookie afterwards.

`/metrics` and `/debug/pprof/*` are **not** in that list: both sit behind the
admin key when enabled. A heap profile exposes internal process state, and
metrics expose traffic shape.

## Trusted proxies

Rate limiting is per client IP, and the client IP comes from the socket unless
`TRUSTED_PROXIES` says otherwise. Empty means loopback only: `X-Forwarded-For`
from anywhere else is ignored.

That is fail-closed on purpose — believing the header by default lets anyone
send `X-Forwarded-For: <random>` and get a fresh rate limit bucket per request.
Behind a proxy, list its subnet, or every request will be counted against the
proxy instead of the caller.
