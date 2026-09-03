---
title: Proxying external images
description: Re-encode somebody else's image at a sane size, without becoming an open proxy.
---

`GET /api/v1/proxy/{name}.{ext}?url=…` fetches an image Falco does not store,
transforms it and serves it. It exists for one situation: third-party images you
want to deliver at your own sizes and formats without copying them into your
storage first — avatars from an identity provider, cover art from a public
catalogue.

```
GET /api/v1/proxy/cover.webp?url=https://cf.geekdo-images.com/img/abc/photo.jpg&w=300
```

The URL being fetched rides in `?url=`, not in the path. The path segment carries
a **name you choose and an extension that must be a known image format**: the
extension is the output format, and having one in the path is what lets a CDN
cache the result under a plain filename. No extension is a `400
INVALID_EXTENSION`, not a guess.

## The allowlist is the whole point

An image proxy that will fetch any URL is an open proxy: it will happily be aimed
at your metadata service, your internal network, or somebody else's bandwidth
bill. Falco only fetches from hosts in `PROXY_ALLOWED_HOSTS`:

```bash
PROXY_ALLOWED_HOSTS=lh3.googleusercontent.com,cf.geekdo-images.com,geekdo-images.com
```

Unset, it falls back to a compiled-in list of the hosts birdple uses — which is
almost certainly not what you want in your deployment. Set it explicitly.

On top of the allowlist, the fetch refuses private, loopback and link-local
addresses (so an allowed host that resolves to `10.0.0.1` gets nowhere), caps the
body at 10 MB, and retries with exponential backoff behind a circuit breaker.

## A narrower parameter set

The proxy accepts `w`, `h`, `q`, `f`, `fit`, `orient` and `meta` — no gravity, no
padding, no trim, no crop. It re-encodes somebody else's image at a sane size; it is not a
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
| 400 | `MISSING_SEGMENT`, `INVALID_EXTENSION` | The path carries no name, or an extension that is not a known image format |
| 400 | `MISSING_URL`, `INVALID_URL`, `URL_TOO_LONG` | `?url=` is absent, is not an absolute `http`/`https` URL, or is too long |
| 403 | `HOST_NOT_ALLOWED` | The host is not in the allowlist, or it resolves to a private, loopback or link-local address |

The allowlist is checked **before** the address is resolved. DNS resolution is
itself an outbound request, so doing it first would let anyone make Falco resolve
arbitrary names.

## Caching

Proxy responses are cached in the same LRU as transformations and carry a fixed
`max-age` of one day and `s-maxage` of thirty — the same values on a hit and on a
miss, so the resource never emits a different `Cache-Control` depending on the
LRU's state.
