---
title: What Falco is
description: A self-hosted image service that stores originals, transforms them on the way out, and refuses to serve a transformation nobody authorised.
---

Falco is a single Go binary that does three things: it **accepts** images, it
**stores** them somewhere you choose, and it **transforms them on the way out**,
at request time, from a URL.

```
POST /api/v1/upload          →  re-encoded, hashed, stored, id returned
GET  /api/v1/images/{id}?w=400&f=webp  →  resized, re-encoded, cached, served
```

The pixels are libvips' work. The bytes live in the filesystem, in S3, in
Cloudflare R2, or in [Jay](https://github.com/ivangsm/jay) over its native
protocol — one process, one config file, no database and no message broker.

## What that buys you

**One service instead of two.** The common alternative is object storage on one
side and a transformation proxy on the other, with an upload path you wrote
yourself in between. Falco is the upload path, the storage abstraction and the
transformer, sharing one authorization model. A key that cannot read a bucket
cannot sign a URL for it either.

**Uploads that deduplicate themselves.** The id of an image is the hash of its
bytes, so uploading the same file twice lands on the same key. There is no
"check if it exists first" round trip to get wrong.

**Delivery a browser can actually use.** The delivery route authorises itself —
by HMAC signature, or by API key plus scope — because an `<img>` tag cannot
attach an `Authorization` header. That is the whole reason it sits outside the
authenticated route group.

**Transforms that a CDN can cache.** The format can ride in the path extension
(`/images/abc123.webp`) instead of the query string, and `maxage` / `smaxage`
set the browser and CDN TTLs per request.

## What it is not

- **Not a general image editor.** Resize, crop, rotate, flip, colour, blur,
  sharpen, trim, padding and watermarking are all there, but as a fixed set of
  URL parameters applied in a fixed order — not a pipeline you compose. See
  [Transformations](/falco/reference/transformations/).
- **Not a video or PDF service.** Non-image files upload and serve untouched
  (see [File passthrough](/falco/guides/uploading/#file-passthrough)), but nothing
  transforms them.
- **Not animated.** GIF input works; the output is a still frame.
- **Not a database.** Falco keeps no state of its own beyond an in-memory cache.
  Everything durable lives in the storage backend.
- **Not static.** libvips is a C library linked with cgo, so the binary is not a
  static one: the host needs libvips 8.18 installed. The container image already
  has it.
