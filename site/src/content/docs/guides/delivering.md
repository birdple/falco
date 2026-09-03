---
title: Delivering images
description: One GET, two internal paths, and why the format can ride in the file extension.
---

```
GET /api/v1/images/{id}[.ext]?[transformations]
```

That is the whole delivery API. Every size, crop and encoding is a URL — nothing
is pre-generated at upload time, and there is no second endpoint to call.

```bash
# As stored
curl -o photo.webp localhost:8080/api/v1/images/a1b2c3d4

# 400px wide
curl -o thumb.webp "localhost:8080/api/v1/images/a1b2c3d4?w=400"

# 300×300, cropped where libvips finds the most detail
curl -o square.webp "localhost:8080/api/v1/images/a1b2c3d4?w=300&h=300&gravity=smart"
```

The full parameter list is in [Transformations](/falco/reference/transformations/).

## What you can ask for

| | |
|---|---|
| **Size and framing** | `w`, `h`, `fit`, `gravity` — including libvips' attention and entropy crops |
| **Cropping and orientation** | `crop_x`, `crop_y`, `crop_w`, `crop_h`, `rotate`, `flip` |
| **Colour and effects** | `brightness`, `contrast`, `gamma`, `saturation`, `hue`, `blur`, `sharpen` |
| **Trim and padding** | `trim`, `trim_threshold`, `pad_*`, `pad_color` |
| **Encoding** | `q`, `f`, and the path extension |
| **Watermark** | `wm` or `wm_url`, `wm_opacity`, `wm_position`, `wm_scale` — see [Watermarks](/falco/guides/watermarks/) |
| **Caching** | `maxage`, `smaxage` |

They are applied in a fixed order — geometry, resize, colour, padding, watermark
— which is what makes a given URL mean exactly one image. Falco is not a
pipeline you compose; it is a set of parameters with a defined order.

```bash
# Crop a region, then deliver it at 200px
curl -o crop.webp "localhost:8080/api/v1/images/a1b2c3d4?crop_x=100&crop_y=50&crop_w=600&crop_h=600&w=200"

# Quarter turn (exact, not interpolated) and a colour lift
curl -o warm.webp "localhost:8080/api/v1/images/a1b2c3d4?rotate=90&saturation=25&brightness=10"
```

## Directories are part of the id

If you uploaded with `?d=avatars`, the object is at `avatars/a1b2c3d4`:

```bash
curl "localhost:8080/api/v1/images/avatars/a1b2c3d4?w=96&h=96"
```

Add `?b=` (or `?storage=`) when the image is not in the default bucket.

## The extension is the format

`/images/a1b2c3d4.webp` and `/images/a1b2c3d4?f=webp` produce the same bytes. The
extension exists because it lets a CDN key its cache on a plain path, with no
query-string normalisation rules to get wrong, and because `<img src>` in the
wild is easier to read that way.

An unknown extension is **rejected**, not guessed at: `/images/a1b2c3d4.bmp`
comes back `400 INVALID_ID` rather than quietly serving WebP under a name that
promises otherwise. A dot inside a directory name (`dir.v2/a1b2c3d4`) is not an
extension and is left alone.

`?f=` wins over the extension when both are present.

## Two paths through the handler

**Without any transformation** — no `w`, `h`, `q`, `f`, no extension — Falco
streams the stored object straight from the backend and does **not** cache it.
There is no CPU work to amortise, and streaming keeps memory flat regardless of
file size.

**With one** — the cache key is computable from the query string alone, before
any storage I/O. A hit answers without ever contacting the backend. On a miss,
concurrent requests for the same key share a single fetch, decode and encode
rather than each doing their own.

That difference is why an id served raw and the same id served at `?w=1200` behave
differently under load, and why the cache metrics only ever move for the second.

## Cache headers

`Cache-Control` carries `max-age` for browsers and `s-maxage` for CDNs, from
`CACHE_DEFAULT_MAX_AGE` and `CACHE_DEFAULT_SMAX_AGE`, plus `immutable`:

```
Cache-Control: public, max-age=31536000, s-maxage=31536000, immutable
```

`immutable` is honest here — the id is the hash of the content, so a given URL
can never describe different bytes. Replacing an image through
`/api/v1/update` is the one exception, and it is why that endpoint deserves
thought on a CDN-fronted deployment.

Override per request:

```
?w=400&maxage=3600&smaxage=604800
```

A malformed value falls back to the default instead of failing the request — the
useful response is still the image.

## When it does not work

| Status | Code | Meaning |
|---|---|---|
| 400 | `INVALID_WIDTH`, `INVALID_HEIGHT`, `INVALID_QUALITY`, `INVALID_FORMAT`, `INVALID_FIT`, `INVALID_CROP`, `INVALID_ROTATE`, `INVALID_FLIP` | A parameter that changes geometry or encoding was malformed. Falco fails rather than serve a different image than the one asked for |
| 403/404/422/502 | `WATERMARK_*` | A watermark was asked for and could not be loaded |
| 400 | `INVALID_ID` | The id, directory or extension does not parse |
| 401 | `UNAUTHORIZED` | `HMAC_REQUIRED=false` and `API_KEY_REQUIRED=true`, and no key was sent |
| 403 | `INVALID_SIGNATURE` | `HMAC_REQUIRED=true` and the `sig` is missing, wrong, or expired |
| 404 | — | No such object in that bucket |
| 500 | `CONFIG_ERROR` | `HMAC_REQUIRE_EXPIRY` is unset or unparseable. Deliberate: the fallback would be accepting signed URLs that never expire |

Cosmetic parameters — `gravity`, `maxage`, `pad_*`, `trim`, `orient`, `meta` and
the whole colour and effects group — never fail a request: a bad value leaves
the default in place. The watermark is the exception among the optional ones: if
it was asked for and could not be loaded, the request fails (`403`, `404`, `422`
or `502`) rather than serving an image that silently has no overlay.
