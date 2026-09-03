---
title: Falco vs imgproxy
description: Both are libvips services in Go. They answer different questions, and this page is honest about which one Falco does not answer.
---

[imgproxy](https://imgproxy.net) is the obvious comparison: same language, same
image library, overlapping purpose. The difference is not quality, it is scope.

**imgproxy transforms images it does not own.** You give it a URL, it fetches,
processes and serves. Storage is your problem, and it is very good at the part it
does.

**Falco owns the images too.** Upload, deduplicate, organise into buckets and
directories, list, delete, back up, and transform on the way out — with one
authorization model over all of it.

If you already have object storage and an upload path you are happy with, the
honest recommendation is imgproxy. Falco earns its place when the upload path is
the thing you do not want to write again.

## Where Falco does more

| | Falco | imgproxy |
|---|---|---|
| Image storage | Built in: filesystem, S3, R2, Jay | None — processes remote URLs |
| Upload API | Yes, with content-hash deduplication | No |
| Management | List, delete, directories, buckets | No |
| Multi-target backups | sync, async, read-fallback | No |
| Scoped API keys | Bucket, group and subgroup level | No (read-only service) |
| Admin panel | Server-rendered, included | Pro only |
| File passthrough | Any non-image type, stored verbatim | No |
| OpenAPI spec | Yes | No |

## Where imgproxy does more

| | Falco | imgproxy |
|---|---|---|
| Animated GIF/WebP | Static frame only | Yes |
| PDF and video thumbnails | No | Yes (video is Pro) |
| Chained pipelines | No | Pro |
| Watermark from an arbitrary URL | Allowlisted hosts only | Yes (advanced is Pro) |
| SVG input | Rejected on purpose | Yes |
| Deployment track record | Young | Widely deployed, commercially supported |

### About the watermark

Both services composite an overlay. Falco will only fetch one from a host listed
in `WATERMARK_ALLOWED_HOSTS`, and refuses external overlays outright when that
variable is unset. The intended source is an image you already uploaded to Falco
(`wm=logos/brand`), which costs no outbound request at all.

### About SVG

Falco refuses SVG uploads with `415`. SVG can carry script, and serving it from
the image origin would execute that script with the origin's privileges. That is
a deliberate trade, not a missing feature — if you need to serve SVGs, serve them
from somewhere that is not also your image transformation service.

## Comparable

Resize with cover/contain/fill, gravity-aware and attention-based cropping,
manual crop, rotation and flip, brightness, contrast, gamma, saturation, hue,
blur and sharpen, trim, padding, quality and format conversion including AVIF,
EXIF auto-orientation and metadata stripping, HMAC URL signing with a required
mode, SSRF protection, trusted proxies, Prometheus metrics, structured logging
and pprof — both do all of it.

Rate limiting is built into Falco and is a Pro feature in imgproxy. Redis caching
likewise.

## Licensing

Falco is GPLv3. imgproxy is MIT for the open-source build with a commercial Pro
edition. If you intend to modify Falco and ship it as part of a product, the
GPLv3 is the thing to read before you start.
