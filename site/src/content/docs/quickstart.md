---
title: Quickstart
description: Run Falco, upload an image, and transform it — in about two minutes.
---

## Run it

```bash
docker run -p 8080:8080 \
  -v falco-data:/app/data \
  -e STORAGE_DEFAULT=local \
  -e STORAGE_BUCKET_LOCAL_TYPE=filesystem \
  -e STORAGE_BUCKET_LOCAL_PATH=/app/data/images \
  -e API_KEY_REQUIRED=false \
  -e HMAC_REQUIRED=false \
  ghcr.io/birdple/falco:latest
```

Those five variables are the minimum, and none of them has a default:

- **`STORAGE_DEFAULT` and the bucket it names.** Falco refuses to start without a
  bucket. There is no implicit "./data/images" fallback — a service that invents
  its own storage location is a service that silently writes your images
  somewhere you did not choose.
- **`API_KEY_REQUIRED` and `HMAC_REQUIRED`.** Both are off here because this is a
  local trial. In any deployment reachable from outside, turn them on — see
  [Authentication](/falco/reference/authentication/).

Check it answered:

```bash
curl -s localhost:8080/health
# {"status":"healthy","version":"0.13.0","uptime":"1.2s"}
```

## Upload an image

```bash
curl -X POST localhost:8080/api/v1/upload -F "file=@photo.jpg"
```

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

The upload was re-encoded to WebP on the way in — `DEFAULT_FORMAT` decides that,
and the **original is not kept**. Pass `?format=jpeg` or `?format=png` if you
need something else stored. The id is the hash of what you sent, so uploading the
same file again returns the same id instead of a second copy.

## Transform it

Everything from here is a URL. No second API call, no pre-generated sizes.

```bash
# A 400px-wide WebP thumbnail
curl -o thumb.webp "localhost:8080/api/v1/images/a1b2c3d4e5f6?w=400&f=webp"

# A 300×300 square, cropped to whatever libvips finds interesting
curl -o square.webp "localhost:8080/api/v1/images/a1b2c3d4e5f6?w=300&h=300&gravity=smart"

# Format from the extension instead of the query string — CDN-friendly
curl -o photo.avif "localhost:8080/api/v1/images/a1b2c3d4e5f6.avif?w=1200"
```

Each distinct combination is computed once and cached in memory; concurrent
requests for the same one share a single decode and encode.

## Look at what you have

Falco ships a server-rendered admin panel at `/` — log in with the API key and
browse the buckets. The same listing is available as JSON:

```bash
curl "localhost:8080/api/v1/list?d=avatars"
```

## Then what

- Turn on auth before anything reaches the internet: [Authentication](/falco/reference/authentication/)
- Sign your delivery URLs so nobody can mint transformations on your CPU: [Signed URLs](/falco/guides/signed-urls/)
- Point it at S3, R2 or Jay instead of the local disk: [Buckets and groups](/falco/guides/buckets/)
