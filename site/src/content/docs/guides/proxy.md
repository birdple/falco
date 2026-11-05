---
title: Proxying external images
description: Re-encode somebody else's image at a sane size, without becoming an open proxy.
---

`GET /api/v1/proxy/{name}[.ext]?url=…` fetches an image Falco does not store,
transforms it and serves it. It exists for one situation: third-party images you
want to deliver at your own sizes and formats without copying them into your
storage first — avatars from an identity provider, cover art from a public
catalogue.

```
GET /api/v1/proxy/cover.webp?url=https://cf.geekdo-images.com/img/abc/photo.jpg&w=300
```

The URL being fetched rides in `?url=`, not in the path. The path segment carries
a **name you choose and, optionally, an extension**: the extension is the output
format (unless `?f=` says otherwise), and having one in the path is what lets a
CDN cache the result under a plain filename. A segment with no extension is
encoded in `DEFAULT_FORMAT`; an extension that is not a known image format
(`webp`, `jpg`, `jpeg`, `png`, `avif`) is a `400 INVALID_EXTENSION`, not a
guess.

## It is signed like delivery

The proxy is authorised exactly like [delivery](/falco/guides/delivering/), and
before anything else: with `HMAC_REQUIRED=true` every proxy URL needs a valid
`sig` (and `exp`, under `HMAC_REQUIRE_EXPIRY=true`) covering its path and query,
`?url=` included. Left unsigned, anyone could make Falco fetch, decode and encode
every `w`×`q`×`f` combination of an allowlisted image, competing with real
delivery for workers and cache.

Sign proxy paths through [`/api/v1/sign`](/falco/guides/signed-urls/) or in your
own process. Since a proxy URL reads no bucket, any authenticated key may sign
one, scoped or not:

```bash
curl -X POST localhost:8080/api/v1/sign \
  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \
  -d '{"path": "/api/v1/proxy/cover.webp?url=https://cf.geekdo-images.com/img/abc/photo.jpg&w=300", "expires_in": 86400}'
```

With `HMAC_REQUIRED=false` the proxy is open, like everything else in that
(development-only) configuration.

## The allowlist is the whole point

An image proxy that will fetch any URL is an open proxy: it will happily be aimed
at your metadata service, your internal network, or somebody else's bandwidth
bill. Falco only fetches from hosts in `PROXY_ALLOWED_HOSTS`:

```bash
PROXY_ALLOWED_HOSTS=lh3.googleusercontent.com,cf.geekdo-images.com,geekdo-images.com
```

Unset, it falls back to a compiled-in list of the hosts birdple uses — which is
almost certainly not what you want in your deployment. Set it explicitly.

On top of the allowlist, the fetch refuses to dial private, loopback and
link-local addresses (so an allowed host that resolves to `10.0.0.1` gets
nowhere — the check runs on the address actually dialled, which also defeats DNS
rebinding), follows redirects only to hosts on the same allowlist, caps the body
at 10 MB and gives up after 10 seconds. It is a single attempt: there are no
retries and no circuit breaker on proxy fetches, so a failing upstream costs one
fetch per request (minus what the negative cache below absorbs).

The upstream answer has to be a raster image: a `Content-Type` that is not
`image/*`, or is SVG, is refused.

## A narrower parameter set

The proxy accepts `w`, `h`, `q`, `f`, `fit`, `orient` and `meta`, validated
exactly as on delivery — no gravity, no padding, no trim, no crop, no
watermark. It re-encodes somebody else's image at a sane size; it is not a
general-purpose editor pointed at third-party CDNs.

Two defaults differ from [delivery](/falco/guides/delivering/):

- **With neither `w` nor `h`, width is capped** at `PROXY_MAX_WIDTH` (600), so a
  cache miss on a full-resolution original does not push a huge file to a browser.
  Height stays unset, so libvips scales proportionally and never crops.
- **Quality defaults to `PROXY_DEFAULT_QUALITY`** (75) rather than delivery's 85.
  These images come from external CDNs and are not archival.

## When it refuses

| Status | Code | Meaning |
|---|---|---|
| 403 | `INVALID_SIGNATURE` | `HMAC_REQUIRED=true` and the `sig` is missing, wrong or expired |
| 500 | `CONFIG_ERROR` | `HMAC_REQUIRE_EXPIRY` is unset or unparseable |
| 400 | `MISSING_SEGMENT`, `INVALID_EXTENSION` | The path carries no name, or an extension that is not a known image format |
| 400 | `MISSING_URL`, `INVALID_URL`, `URL_TOO_LONG` | `?url=` is absent, is not an absolute `http`/`https` URL, or is over 2048 characters |
| 400 | `INVALID_WIDTH`, `INVALID_HEIGHT`, `INVALID_QUALITY`, `INVALID_FORMAT`, `INVALID_FIT` | A malformed parameter, as on delivery |
| 403 | `HOST_NOT_ALLOWED` | The host is not in the allowlist |
| 502 | `FETCH_FAILED` | The fetch failed: unreachable, timed out, or the host resolved to a private, loopback or link-local address |
| 502 | `UPSTREAM_ERROR` | The upstream answered with a non-2xx status |
| 415 | `NOT_AN_IMAGE` | The upstream did not answer with a raster image (SVG included) |
| 413 | `IMAGE_TOO_LARGE` | The upstream body is over 10 MB |
| 415/422/503 | `UNSUPPORTED_IMAGE`, `IMAGE_TOO_LARGE`, `PROCESSING_FAILED`, `PROCESSING_BUSY` | Decoding or encoding failed, as on delivery |

The allowlist is checked **before** the address is resolved, and nothing is
resolved on a request that is answered from cache. DNS resolution is itself an
outbound request, so doing it first would let anyone make Falco resolve
arbitrary names.

## Caching

Proxy responses are cached in the same cache as transformations, under a key
built the same way — the URL plus every parameter that changes the bytes,
`orient` and `meta` included. They carry a fixed `max-age` of one day and
`s-maxage` of thirty — the same values on a hit and on a miss, so the resource
never emits a different `Cache-Control` depending on the cache's state.
`maxage`/`smaxage` are not read here.

Failures that are a stable property of the URL — an upstream `404` or `410`, a
response that is not an image, a body over 10 MB — are remembered for three
minutes, **by URL**, so a crawler hammering a dead link does not cost a fetch per
request, and varying `?w=` does not get around it. Timeouts, connection errors,
upstream `5xx`/`429` and processing failures are not remembered: they can be
transient, and caching them would keep serving an error after the cause is gone.
