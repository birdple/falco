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
[proxy](/falco/guides/proxy/): private and loopback addresses are refused at
dial time and the body is capped at `MAX_FILE_SIZE_MB`. Unlike the proxy, a
transient failure is retried with exponential backoff. A URL that cannot be
fetched is `400 DOWNLOAD_FAILED`.

## Where it lands

| Parameter | Aliases | Meaning |
|---|---|---|
| `b` | `bucket` | Bucket to write to |
| `storage` | — | Same idea, kept for existing callers |
| `d` | `dir`, `directory` | Directory inside the bucket |
| `id` | — | Your own id instead of the content hash. Letters, digits, `-` and `_`, up to 100 characters |
| `quality` | — | Encode quality, 1–100 |
| `format` | — | Stored format: `jpeg`, `png`, `webp`, `avif`, `heic` |

`quality`, `format` and `id` also work as multipart fields or JSON keys, which is
usually more convenient than the query string. For a raw-body upload they can
only come from the query string.

Without `b`, the write goes to `storage.default`. A key scoped to one bucket
cannot write to another — the attempt comes back `403 ACCESS_DENIED` — and that
includes the default bucket: leaving `b` out does not get around a scope.

With a `b` that names neither a bucket nor a [declared
alias](/falco/guides/buckets/), the upload is refused with
`400 UNKNOWN_BUCKET` and **nothing is written**. Falco does not fall back to
the default bucket: it used to, answering `201` with the object somewhere the
caller never named.

## What happens to the bytes

**The original is not kept.** An image is decoded, re-encoded to
`DEFAULT_FORMAT` (WebP unless you changed it) or to the `format` you asked for,
and only the result is stored. If you need the untouched file, store the
original elsewhere.

**Metadata is stripped and orientation applied.** The re-encode rotates the
pixels according to the EXIF orientation and then drops EXIF, XMP and IPTC —
GPS coordinates included — so the stored image, and every raw delivery of it,
carries no location. A delivery-time `meta=1` cannot bring back what the upload
removed.

**Inputs are limited.** libvips only decodes JPEG, PNG, WebP, GIF, HEIF/HEIC,
AVIF and TIFF here, and refuses an image over `MAX_MEGAPIXELS` (100 million
pixels by default). A failure is `422 PROCESSING_FAILED`.

**The id is the hash of what you sent.** Upload the same bytes twice and you get
the same id, with no second copy written. This is why there is no "does it exist
already" call in the API: the answer is the id itself.

Uploading under a key that already exists — always the case with a custom `id`
reused — replaces the object and drops its cached transformations, so the next
delivery renders the new bytes.

## Owners

Send `X-Owner-Id` (an opaque string, typically a user id from your own service)
and it is stored with the object. It only matters to **scoped** keys:

- `update` and `delete` by a scoped key need the same `X-Owner-Id` as the stored
  owner. An object with no owner can only be changed by the admin key.
- A scoped upload over an object someone else owns is refused with
  `409 OBJECT_EXISTS` when it used a custom `id`. With a content-hash id the
  bytes are identical by definition, so the existing object is returned
  unchanged — its owner is not taken over by the new caller.

The admin key bypasses all of it. Ownership is recorded on every backend,
S3 and R2 included.

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
refused with `413 REQUEST_TOO_LARGE` rather than truncated.

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
errors. A non-image is stored as-is, so the `format` and `quality` options do not
apply to it.

**Three types are rejected outright** with `415 DANGEROUS_CONTENT_TYPE`: SVG,
HTML and XML. All three can carry script, and serving them from Falco's origin
would run that script with Falco's origin's privileges. The content type is
sniffed from the bytes, so renaming the file changes nothing.

## Replacing an image

`POST /api/v1/update` fetches a new version from an external URL and replaces the
stored object under a key you already have:

```bash
curl -X POST localhost:8080/api/v1/update \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"bucket": "images", "key": "avatars/a1b2c3d4e5f6", "url": "https://example.com/better-photo.jpg", "quality": 85}'
```

`url`, `bucket`, `key` and `quality` (1–100) are all required; `format` and
`storage` are optional. Unlike an upload there is nothing to infer: an update
names an existing object. A scoped key's ownership is checked **before** the new
image is downloaded, and the stored owner is kept.

```json
{
  "success": true,
  "updated": [
    { "key": "avatars/a1b2c3d4e5f6", "url_size": 912384, "bucket_size": 842103,
      "new_size": 401220, "saved_bytes": 440883, "saved_percent": 52.35,
      "format": "webp", "quality": 85 }
  ]
}
```

Cached transformations of that key are invalidated. URLs already handed out keep
working and start serving the new image — which is the point, and also the
reason to think twice before using it on a CDN-fronted deployment where the old
bytes may live on for as long as `s-maxage`.
