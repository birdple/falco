---
title: Watermarks
description: Composite a logo onto every render — from your own storage, or from an allowlisted host, and never silently missing.
---

A watermark is two decisions: where the overlay comes from, and what happens
when it cannot be loaded. Falco answers the second one first — **a watermark
that fails is an error**, never an image quietly served without it.

```
GET /api/v1/images/a1b2c3d4?w=800&wm=logos/brand
```

## Where the overlay comes from

**From your own storage — the intended path.** Upload the logo to Falco like any
other image, then name it:

```bash
curl -X POST "localhost:8080/api/v1/upload?d=logos&id=brand" \
  -H "X-API-Key: $KEY" -F "file=@logo.png"

curl -o out.webp "localhost:8080/api/v1/images/a1b2c3d4?w=800&wm=logos/brand"
```

No configuration, no outbound request. The overlay is read through the **same
backend as the image itself**, so a key scoped to one bucket cannot pull a
watermark out of another.

**From an external URL — opt-in, allowlisted.**

```bash
WATERMARK_ALLOWED_HOSTS=cdn.example.com,assets.example.com
```

```
?w=800&wm_url=https%3A%2F%2Fcdn.example.com%2Fbrand.png
```

With `WATERMARK_ALLOWED_HOSTS` unset, `wm_url` is refused with `403`. The empty
list means *no external watermarks*, not *any host*: an image URL that can make
the server fetch an arbitrary address is an open proxy with extra steps. The
host is checked before the name is resolved — DNS resolution is itself an
outbound request — and the client will not dial a private or reserved address
even for an allowed host.

Naming both `wm` and `wm_url` is a `400`. A precedence rule between them is
something nobody would remember correctly.

## Placing it

| Parameter | Values | Default |
|---|---|---|
| `wm_position` | `top-left`, `top-right`, `bottom-left`, `bottom-right`, `center` | `bottom-right` |
| `wm_scale` | 0 … 1, relative to the **final** image width | 0.2 |
| `wm_opacity` | 0 … 1 | 1 (opaque) |

```
?w=800&wm=logos/brand&wm_position=bottom-right&wm_scale=0.15&wm_opacity=0.6
```

The scale is relative to the width the viewer actually receives, so one URL
pattern gives a proportionate logo on a 200px thumbnail and on a 2000px render
alike. The overlay is inset from the edge by 2% of the width, with a floor so it
stays visible on small renders.

An opacity of `0` means "not specified" rather than "invisible": a request that
went to the trouble of naming a watermark did not mean to make it disappear, and
a caller who wants none simply omits it.

## Where it lands in the pipeline

Last — after resize, after padding. Two reasons, and both are visible in the
output if you get them wrong:

- **After resize**, because `wm_scale` is relative to the delivered width. Scale
  it before and every size would carry a differently-proportioned logo.
- **After the colour adjustments**, because a logo that went through
  `saturation` or `hue` would come out tinted. A brand mark that changes colour
  with the photo behind it is not a brand mark.

## When it cannot be loaded

| Status | Code | Cause |
|---|---|---|
| 400 | `INVALID_WATERMARK` | Both sources named, or a `wm` that is not a valid id or escapes its directory |
| 403 | `WATERMARK_HOST_NOT_ALLOWED` | `wm_url` host is not allowlisted, the allowlist is empty, or the host resolves inward |
| 404 | `WATERMARK_NOT_FOUND` | `wm` names an image that is not in the bucket |
| 422 | `WATERMARK_NOT_AN_IMAGE` | The overlay is not an image |
| 422 | `WATERMARK_TOO_LARGE` | The overlay is over 2 MB. A logo is kilobytes |
| 502 | `WATERMARK_FETCH_FAILED` | The allowlisted host did not serve it |

None of these degrades into an unwatermarked image. That response would be
indistinguishable from one that worked — the caller asked for a watermark, would
not get one, and nothing in the response would say so.

## Caching

The watermark is part of the cache key, by **source** rather than by bytes: two
requests for different overlays never collide, and Falco does not hash the
overlay on every request to find that out.

The overlay itself is read once and kept in memory for ten minutes, so a logo
that appears on ten thousand images costs one storage read rather than ten
thousand. It is loaded only on a cache miss: a request answered from the
transformed cache never touches the overlay at all.

Replacing the logo at the same id (`POST /api/v1/update`) therefore takes up to
ten minutes to appear on new renders, and does not touch renders already cached.
Publishing under a new id and changing the `wm` parameter is the predictable
move.
