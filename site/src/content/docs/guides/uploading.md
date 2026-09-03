---
title: Uploading
description: Three ways to put bytes into Falco, what it does to them on the way in, and what it refuses.
---

`POST /api/v1/upload` takes an image in one of three shapes and always answers
the same way. It is one of the routes behind the API key — see
[Authentication](/falco/reference/authentication/).

## Three shapes

```bash
# Multipart form
curl -X POST localhost:8080/api/v1/upload \
  -H "X-API-Key: $KEY" \
  -F "file=@photo.jpg"

# Raw body
curl -X POST localhost:8080/api/v1/upload \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: image/jpeg" \
  --data-binary @photo.jpg

# From a URL — Falco fetches it
curl -X POST localhost:8080/api/v1/upload \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/photo.jpg", "format": "webp"}'
```

The URL form goes out through the same guarded HTTP client as the
[proxy](/falco/guides/proxy/): private and loopback addresses are refused, the
body is capped, and the fetch retries with exponential backoff.

## Where it lands

| Parameter | Aliases | Meaning |
|---|---|---|
| `b` | `bucket` | Bucket to write to |
| `storage` | — | Same idea, kept for existing callers |
| `d` | `dir`, `directory` | Directory inside the bucket |
| `id` | — | Your own id instead of the content hash. Letters, digits, `-` and `_`, up to 100 characters |
| `quality` | — | Encode quality, 1–100 |
| `format` | — | Stored format: `jpeg`, `png`, `webp`, `avif` |

`quality` and `format` also work as multipart fields or JSON keys, which is
usually more convenient than the query string.

Without `b`, the write goes to `storage.default`. A key scoped to one bucket
cannot write to another — the attempt comes back `403 ACCESS_DENIED`.

## What happens to the bytes

**The original is not kept.** An image is decoded, re-encoded to
`DEFAULT_FORMAT` (WebP unless you changed it) or to the `format` you asked for,
and only the result is stored. If you need the untouched file, upload it as a
non-image (see below) or store the original elsewhere.

**The id is the hash of what you sent.** Upload the same bytes twice and you get
the same id, with no second copy written. This is why there is no "does it exist
already" call in the API: the answer is the id itself.

```json
{
  "success": true,
  "data": {
    "id": "6d556268ff5afc0f",
    "url": "/api/v1/images/6d556268ff5afc0f",
    "original_name": "photo.jpg",
    "format": "webp",
    "size": 842103,
    "dimensions": { "width": 4032, "height": 3024 },
    "created_at": "2026-09-03T00:33:32Z"
  }
}
```

The id and url are what you store; `dimensions` describes the image **after**
re-encoding, not what you sent.

`MAX_FILE_SIZE_MB` (10 by default) caps the request body. Over it, the upload is
refused rather than truncated.

## File passthrough

Anything that is not an image is stored **byte for byte** — PDFs, archives,
fonts, videos. Falco is an object store as well as an image service, and
re-encoding a PDF would corrupt it.

```bash
curl -X POST localhost:8080/api/v1/upload \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/pdf" \
  --data-binary @contract.pdf
```

They come back from the same delivery route, with their detected content type
and no processing. Transformation parameters on a non-image are ignored, not
errors.

**Three types are rejected outright** with `415 DANGEROUS_CONTENT_TYPE`: SVG,
HTML and XML. All three can carry script, and serving them from Falco's origin
would run that script with Falco's origin's privileges. The content type is
sniffed from the bytes, so renaming the file changes nothing.

## Replacing an image

`POST /api/v1/update` fetches a new version from an external URL and replaces the
stored object under an id you already have:

```bash
curl -X POST localhost:8080/api/v1/update \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"id": "a1b2c3d4e5f6", "url": "https://example.com/better-photo.jpg"}'
```

Cached transformations of that id are invalidated. URLs already handed out keep
working and start serving the new image — which is the point, and also the
reason to think twice before using it on a CDN-fronted deployment where the old
bytes may live on for as long as `s-maxage`.
