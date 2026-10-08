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

`?d=avatars` says the same thing as a query parameter. The directory in the path
is held to the same rules as `?d=`: `..`, an absolute path or an empty segment
is `400 INVALID_ID` before anything reaches storage.

Add `?b=` (or `?storage=`) when the image is not in the default bucket. It
has to name a bucket that exists, or an alias declared for one — anything else
is `400 UNKNOWN_BUCKET`, never a quiet read from the default bucket. See
[Buckets and groups](/falco/guides/buckets/).

## The extension is the format

`/images/a1b2c3d4.webp` and `/images/a1b2c3d4?f=webp` produce the same bytes. The
extension exists because it lets a CDN key its cache on a plain path, with no
query-string normalisation rules to get wrong, and because `<img src>` in the
wild is easier to read that way.

An unknown extension is **rejected**, not guessed at: `/images/a1b2c3d4.bmp`
comes back `400 INVALID_ID` rather than quietly serving WebP under a name that
promises otherwise. A dot inside a directory name (`dir.v2/a1b2c3d4`) is not an
extension and is left alone.

`?f=` wins over the extension when both are present, and accepts the same
spellings — `?f=jpg` is `jpeg`.

## Two paths through the handler

**With a transformation** — any of `w`, `h`, `q`, a crop, rotation, flip,
gravity, colour, trim, padding or a watermark — the cache key is computable from
the query string alone, before any storage I/O. A hit answers without ever
contacting the backend. On a miss, concurrent requests for the same key share a
single fetch, decode and encode rather than each doing their own; a client that
hangs up stops waiting without cancelling the work for the others.

**Without one** — the original as stored, or the original in another format —
Falco starts by streaming from the backend:

- If no format was asked for, or the one asked for (by `?f=` or by extension) is
  the format the object is already stored in, the bytes stream straight through
  and are **not** cached. There is no CPU work to amortise, and streaming keeps
  memory flat regardless of file size. `/images/a1b2c3d4.webp` on an object
  stored as WebP is this case.
- If the format differs, it is a conversion: answered from the cache when it can
  be, and cached once encoded.

That difference is why an id served raw and the same id served at `?w=1200` behave
differently under load, and why the cache metrics only ever move for the second.

## Cache headers

`Cache-Control` carries `max-age` for browsers and `s-maxage` for CDNs, from
`CACHE_DEFAULT_MAX_AGE` and `CACHE_DEFAULT_SMAX_AGE`:

```
Cache-Control: public, max-age=31536000, s-maxage=31536000
```

There is no `immutable`. The id is usually the hash of the content, but
`/api/v1/update` replaces an object's bytes under the same key, and `immutable`
would tell browsers never to revalidate within `max-age`, even on reload. On a
CDN-fronted deployment that endpoint deserves thought.

Override per request on a rendered response:

```
?w=400&maxage=3600&smaxage=604800
```

A malformed value falls back to the default instead of failing the request — the
useful response is still the image. Error responses are never cacheable: they
carry `Cache-Control: no-store`, so a CDN does not keep a `404` for an image that
is uploaded a moment later.

Every response carries an `ETag`, and a rendering carries the same one whether
it was just encoded or came from the cache. Both paths answer conditional
requests with `304`: `If-None-Match` (a list of tags, `W/` tags and `*`
included) and, without it, `If-Modified-Since` against the stored object's
`Last-Modified` on the streaming path. Range requests work on transformed
responses; a stream straight from storage answers `Accept-Ranges: none`.

## When it does not work

| Status | Code | Meaning |
|---|---|---|
| 400 | `INVALID_WIDTH`, `INVALID_HEIGHT`, `INVALID_QUALITY`, `INVALID_FORMAT`, `INVALID_FIT`, `INVALID_CROP`, `INVALID_ROTATE`, `INVALID_FLIP` | A parameter that changes geometry or encoding was malformed. Falco fails rather than serve a different image than the one asked for |
| 400/403/404/422/502 | `INVALID_WATERMARK`, `WATERMARK_*` | A watermark was asked for and could not be loaded |
| 400 | `INVALID_ID`, `INVALID_DIRECTORY` | The id, directory or extension does not parse |
| 400 | `UNKNOWN_BUCKET` | `?b=` names neither a bucket nor a declared alias |
| 403 | `INVALID_SIGNATURE` | `HMAC_REQUIRED=true` and the `sig` is missing, wrong, or expired |
| 404 | `IMAGE_NOT_FOUND` | No such object in that bucket |
| 415 | `UNSUPPORTED_IMAGE` | The stored image is in a format libvips is not allowed to decode |
| 422 | `IMAGE_TOO_LARGE` | The source or the requested output is over `MAX_MEGAPIXELS` |
| 422 | `PROCESSING_FAILED` | libvips could not decode or encode it |
| 500 | `CONFIG_ERROR` | `HMAC_REQUIRE_EXPIRY` is unset or unparseable. Deliberate: the fallback would be accepting signed URLs that never expire |
| 500 | `RETRIEVAL_ERROR` | The backend failed in a way that is not "not found" |
| 503 | `STORAGE_UNAVAILABLE` | The bucket's circuit breaker is open. Retry later |
| 503 | `PROCESSING_BUSY` | No processing slot freed up in time. Retry later |

Cosmetic parameters — `gravity`, `maxage`, `smaxage`, `pad_*`, `trim`, `orient`,
`meta` and the whole colour and effects group — never fail a request: a bad value
leaves the default in place. The watermark is the exception among the optional
ones: if it was asked for and could not be loaded, the request fails (`403`,
`404`, `422` or `502`) rather than serving an image that silently has no overlay.
