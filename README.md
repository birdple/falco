# Falco — High-Performance Image Processing Service

![Falco Service](falco.webp)

A blazing-fast, self-hosted image processing service built in Go. Store, transform, and deliver images with a clean REST API. Designed as a production-ready alternative to managed image CDNs and on-par with open-source tools like imgproxy — but with integrated storage management and a built-in web dashboard.

## Features

### Core Capabilities
- **RESTful API** with Chi router — fast, idiomatic Go routing
- **Multi-format Support** — JPEG, PNG, WebP, AVIF, GIF, TIFF, SVG (input); JPEG, PNG, WebP, AVIF, HEIC (output)
- **Dynamic Transformations** — resize, crop, rotate, flip, filters, padding, trim, watermark, and more on-the-fly
- **Smart Cropping** — gravity-aware cropping with `center`, `north`, `south`, `east`, `west`, `smart` (attention), and `entropy` modes
- **Web Dashboard** — server-rendered UI with HTMX and Tailwind CSS for browsing stored images
- **Image Update** — download, process, and replace images from external URLs with retry/backoff
- **File Management** — list images in buckets/directories, delete files or entire directories
- **Flexible Storage** — local filesystem, MinIO, Amazon S3, or Cloudflare R2 with multi-bucket and directory support
- **File Passthrough** — upload and serve any file type (PDFs, ZIPs, videos, etc.) without processing
- **Bucket Groups** — organize N buckets from N providers into logical groups with shared API keys and 1-level subgroups with inheritance
- **Multi-Target Backups** — each bucket can have multiple backup targets with independent modes (sync, async, read-fallback)
- **Content-based Deduplication** — automatic duplicate detection via hash-based IDs
- **Custom Image IDs** — optional manual ID specification for better organization

### Performance
- **Concurrent processing** with configurable semaphore-based worker limits
- **Dual-layer caching** — in-memory LRU cache + optional persistent Redis cache
- **Buffer pooling** — reuse byte buffers during image processing to reduce GC pressure
- **Configurable TTLs** — separate `Cache-Control` and `s-maxage` for browsers and CDNs

### Security
- **HMAC-SHA256 URL signing** — signed delivery URLs to prevent unauthorized transformations
- **API key authentication** — global admin key + scoped keys at bucket, group, or subgroup level
- **SSRF protection** — blocks requests to private/loopback IP ranges when downloading external URLs
- **Trusted proxies** — validate `X-Forwarded-For` only from known proxy IPs to prevent rate limiter bypass
- **Path traversal protection** — validates storage paths in the filesystem backend
- **Rate limiting** — per-IP request throttling with configurable burst
- **CORS** — configurable allowed origins (defaults to localhost in development, hardened in production)
- **Security headers** — applied globally via middleware

### Observability
- **Prometheus metrics** — requests, latency, cache hits/misses, cache size, item count, evictions, processing size
- **Structured logging** — zerolog with JSON output, configurable level and format
- **Sanitized config dump** — logs all configuration at startup, redacting secrets
- **Health check** endpoint at `/health`
- **OpenAPI documentation** at `/docs`
- **pprof** support for profiling (opt-in)

### Production Ready
- **Docker** with multi-stage builds for minimal image size
- **Docker Compose** profiles including MinIO
- **Graceful shutdown** with configurable timeout
- **Circuit breaker** for external service calls
- **HTTP retry with exponential backoff** for external URL downloads

---

## Quick Start

### Prerequisites
- Go 1.23+
- [libvips](https://www.libvips.org/install.html) (`brew install vips` on macOS)
- Docker & Docker Compose (optional)

### Run Locally

```bash
git clone https://github.com/birdple/falco.git
cd falco

go mod download
go run cmd/server/main.go
```

### Docker

```bash
# Build and run
docker build -t falco .
docker run -p 8080:8080 falco

# With Docker Compose (recommended)
docker-compose up --build

# With MinIO for local object storage
docker-compose --profile with-minio up -d
```

---

## Configuration

Copy `.env.example` to `.env` and adjust values. All settings work via both YAML (`config.yaml`) and environment variables.

### Storage: Buckets & Groups

Falco uses a unified **bucket-group** model. Every storage target is a named bucket. Buckets can be grouped, and each level (bucket, group, subgroup) can have its own API keys.

#### YAML Configuration

```yaml
storage:
  default: "images"   # required: which bucket to use when none is specified

  buckets:
    images:
      type: s3
      bucket: prod-images
      region: us-west-2
      access_key: ""
      secret_key: ""
      backups:
        - target: "images-backup"
          mode: sync
        - target: "images-archive"
          mode: async
      keys:
        - name: "client-a"
          key: "sk-client-a-secret"

    images-backup:
      type: minio
      bucket: backup-images
      endpoint: "http://minio:9000"
      access_key: ""
      secret_key: ""

    images-archive:
      type: r2
      bucket: archive-images
      account_id: ""
      access_key: ""
      secret_key: ""

    local-files:
      type: filesystem
      path: ./data/images

  groups:
    media:
      buckets: ["images", "images-backup", "images-archive"]
      keys:
        - name: "media-team"
          key: "sk-media-team-secret"
      subgroups:
        thumbnails:
          buckets: ["images"]
          keys:
            - name: "thumb-service"
              key: "sk-thumb-service"
```

#### Environment Variables

Buckets, backups, keys, and groups are auto-discovered from env vars:

```bash
# Buckets: STORAGE_BUCKET_<NAME>_TYPE + fields
STORAGE_DEFAULT=images
STORAGE_BUCKET_IMAGES_TYPE=s3
STORAGE_BUCKET_IMAGES_BUCKET=prod-images
STORAGE_BUCKET_IMAGES_REGION=us-west-2
STORAGE_BUCKET_IMAGES_ACCESS_KEY=...
STORAGE_BUCKET_IMAGES_SECRET_KEY=...

# Backups: STORAGE_BUCKET_<NAME>_BACKUP_<N>_TARGET/MODE
STORAGE_BUCKET_IMAGES_BACKUP_1_TARGET=imagesbackup
STORAGE_BUCKET_IMAGES_BACKUP_1_MODE=sync

# Bucket keys: STORAGE_BUCKET_<NAME>_KEY_<KEYNAME>_KEY
STORAGE_BUCKET_IMAGES_KEY_CLIENTA_KEY=sk-client-a-secret

# Groups: STORAGE_GROUP_<NAME>_BUCKETS
STORAGE_GROUP_MEDIA_BUCKETS=images,imagesbackup
STORAGE_GROUP_MEDIA_KEY_MEDIATEAM_KEY=sk-media-team-secret

# Subgroups: STORAGE_GROUP_<NAME>_SUBGROUP_<SUB>_BUCKETS
STORAGE_GROUP_MEDIA_SUBGROUP_THUMBNAILS_BUCKETS=images
STORAGE_GROUP_MEDIA_SUBGROUP_THUMBNAILS_KEY_THUMBSVC_KEY=sk-thumb-svc
```

#### Simple Setup (filesystem only)

If no buckets are configured, Falco defaults to a single filesystem bucket at `./data/images`:

```bash
# This is all you need for a minimal setup
STORAGE_DEFAULT=local
STORAGE_BUCKET_LOCAL_TYPE=filesystem
STORAGE_BUCKET_LOCAL_PATH=./data/images
```

### Other Environment Variables

```bash
# Server
PORT=8080
HOST=0.0.0.0

# Cache
CACHE_SIZE_MB=256
CACHE_TTL_HOURS=24
CACHE_DEFAULT_MAX_AGE=3600       # Browser Cache-Control (seconds)
CACHE_DEFAULT_SMAX_AGE=7200      # CDN s-maxage (seconds)
ENABLE_REDIS=false
REDIS_URL=redis://localhost:6379/0

# Processing
MAX_FILE_SIZE_MB=10
DEFAULT_QUALITY=80
DEFAULT_FORMAT=webp
CONCURRENT_WORKERS=4

# Security
API_KEY_REQUIRED=true
API_KEY=your-api-key
CORS_ORIGINS=https://yourdomain.com
RATE_LIMIT_RPM=1000

# HMAC URL signing (recommended for production)
HMAC_KEY=                        # openssl rand -hex 32
HMAC_SALT=
HMAC_SIGNATURE_SIZE=32
HMAC_REQUIRED=false

# Observability
LOG_LEVEL=info
LOG_FORMAT=json
ENABLE_METRICS=true
ENABLE_PPROF=false
```

---

## API Reference

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/` | — | Web dashboard |
| `GET` | `/health` | — | Health check |
| `GET` | `/docs` | — | OpenAPI UI |
| `GET` | `/docs/openapi.yaml` | — | OpenAPI spec |
| `GET` | `/metrics` | API key* | Prometheus metrics |
| `GET` | `/api/v1/images/*` | — | Deliver/transform image |
| `POST` | `/api/v1/upload` | API key* | Upload image |
| `POST` | `/api/v1/update` | API key* | Replace image from URL |
| `GET` | `/api/v1/list` | API key* | List stored images |
| `DELETE` | `/api/v1/delete` | API key* | Delete image or directory |
| `POST` | `/api/v1/sign` | API key* | Generate signed delivery URL |

*Required when `API_KEY_REQUIRED=true`

---

### Authentication

Pass the API key in either header:

```bash
X-API-Key: your-api-key
# or
Authorization: Bearer your-api-key
```

---

### Upload Image

**`POST /api/v1/upload`**

Supports multipart form-data, raw binary, and URL-based uploads.

#### Query Parameters

| Param | Description |
|-------|-------------|
| `b` / `bucket` | Bucket name |
| `d` / `dir` | Directory path within bucket |
| `id` | Custom image ID (alphanumeric, hyphens, underscores, max 100 chars) |
| `quality` | Output quality 1–100 |
| `format` | Output format: `jpeg`, `png`, `webp`, `avif` |

```bash
# Multipart upload
curl -X POST http://localhost:8080/api/v1/upload \
  -H "X-API-Key: your-key" \
  -F "file=@image.jpg"

# With bucket, directory, and format
curl -X POST "http://localhost:8080/api/v1/upload?b=products&d=electronics" \
  -H "X-API-Key: your-key" \
  -F "file=@photo.jpg" \
  -F "format=webp" \
  -F "quality=85"

# Raw binary upload
curl -X POST http://localhost:8080/api/v1/upload \
  -H "X-API-Key: your-key" \
  -H "Content-Type: image/jpeg" \
  --data-binary "@image.jpg"

# URL-based upload
curl -X POST http://localhost:8080/api/v1/upload \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/photo.jpg", "format": "webp"}'
```

**Response:**

```json
{
  "id": "a1b2c3d4",
  "url": "/api/v1/images/a1b2c3d4",
  "format": "webp",
  "width": 1920,
  "height": 1080,
  "size": 124500
}
```

---

### Deliver & Transform Image

**`GET /api/v1/images/{id}`**

Transforms are applied on-the-fly and cached. The delivery endpoint is always public.

#### Resize & Crop

| Param | Description |
|-------|-------------|
| `w` | Width in pixels |
| `h` | Height in pixels |
| `fit` | Resize mode: `cover` (default), `contain`, `fill` |
| `gravity` | Crop anchor: `center`, `north`, `south`, `east`, `west`, `smart`, `entropy` |
| `crop_x`, `crop_y` | Manual crop origin (pixels) |
| `crop_w`, `crop_h` | Manual crop size (pixels) |

#### Adjustments

| Param | Description |
|-------|-------------|
| `quality` | Output quality 1–100 |
| `format` | Output format: `jpeg`, `png`, `webp`, `avif` |
| `rotate` | Rotation angle in degrees |
| `flip` | `horizontal` or `vertical` |
| `auto_orient` | Auto-rotate from EXIF data (`true`/`false`) |
| `strip_metadata` | Remove EXIF/ICC metadata (`true`/`false`) |

#### Filters

| Param | Range | Description |
|-------|-------|-------------|
| `brightness` | -100 to 100 | Brightness adjustment |
| `contrast` | -100 to 100 | Contrast adjustment |
| `gamma` | 0.0 to 3.0 | Gamma correction |
| `saturation` | -100 to 500 | Color saturation |
| `hue` | -180 to 180 | Hue rotation |
| `blur` | 0.0 to 100.0 | Gaussian blur |
| `sharpen` | 0.0 to 100.0 | Sharpening |

#### Trim & Padding

| Param | Description |
|-------|-------------|
| `trim` | `true` to remove uniform color borders |
| `trim_threshold` | Color tolerance 0–255 (default 10) |
| `pad_top`, `pad_right`, `pad_bottom`, `pad_left` | Padding in pixels |
| `pad_color` | Padding hex color (default `FFFFFF`) |

#### Watermark

| Param | Description |
|-------|-------------|
| `wm_url` | URL of watermark image |
| `wm_opacity` | Watermark opacity 0.0–1.0 |
| `wm_position` | `top-left`, `top-right`, `bottom-left`, `bottom-right`, `center` |
| `wm_scale` | Watermark scale relative to image width (0.0–1.0) |

#### Cache Headers

| Param | Description |
|-------|-------------|
| `maxage` | `Cache-Control: max-age` in seconds |
| `smaxage` | `Cache-Control: s-maxage` for CDNs in seconds |

```bash
# Resize and convert to WebP
GET /api/v1/images/a1b2c3d4?w=800&h=600&fit=cover&format=webp

# Smart crop with gravity
GET /api/v1/images/a1b2c3d4?w=400&h=400&gravity=smart

# Thumbnail with brightness boost and WebP
GET /api/v1/images/a1b2c3d4?w=200&h=200&brightness=10&format=webp&quality=80

# Portrait crop from top
GET /api/v1/images/a1b2c3d4?w=600&h=800&fit=cover&gravity=north

# Trim whitespace and add 20px padding
GET /api/v1/images/a1b2c3d4?trim=true&trim_threshold=15&pad_top=20&pad_bottom=20&pad_color=F5F5F5
```

---

### HMAC URL Signing

When `HMAC_REQUIRED=true`, delivery requests must include a valid signature. Use the sign endpoint to generate signed URLs.

**`POST /api/v1/sign`**

```bash
curl -X POST http://localhost:8080/api/v1/sign \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"path": "/api/v1/images/a1b2c3d4?w=800&format=webp"}'
```

```json
{
  "signed_url": "/api/v1/images/a1b2c3d4?w=800&format=webp&sig=abc123..."
}
```

---

### Update Image from URL

**`POST /api/v1/update`**

Downloads an image from an external URL (with SSRF protection and automatic retry with exponential backoff), processes it, and replaces the stored image.

```bash
curl -X POST http://localhost:8080/api/v1/update \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"id": "a1b2c3d4", "url": "https://example.com/new-photo.jpg"}'
```

---

### List Images

**`GET /api/v1/list`**

```bash
# List all images
curl -H "X-API-Key: your-key" http://localhost:8080/api/v1/list

# List in a specific bucket and directory
curl -H "X-API-Key: your-key" "http://localhost:8080/api/v1/list?b=products&d=electronics"
```

---

### Delete

**`DELETE /api/v1/delete`**

```bash
# Delete a single image
curl -X DELETE http://localhost:8080/api/v1/delete \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"id": "a1b2c3d4"}'

# Delete entire directory
curl -X DELETE "http://localhost:8080/api/v1/delete?d=products/thumbnails" \
  -H "X-API-Key: your-key"
```

---

## MinIO Setup

```bash
# Start with Docker Compose
docker-compose --profile with-minio up -d

# Access MinIO Console: http://localhost:9001
# Default credentials: minioadmin / minioadmin

STORAGE_DEFAULT=minio
STORAGE_BUCKET_MINIO_TYPE=minio
STORAGE_BUCKET_MINIO_BUCKET=images
STORAGE_BUCKET_MINIO_ENDPOINT=http://localhost:9000
STORAGE_BUCKET_MINIO_ACCESS_KEY=minioadmin
STORAGE_BUCKET_MINIO_SECRET_KEY=minioadmin
STORAGE_BUCKET_MINIO_SECURE=false
```

---

## Multiple Buckets

Run multiple storage buckets from different providers simultaneously. Select a bucket per request with `?storage=`:

```bash
# Upload to the "cdn" bucket
curl -X POST "http://localhost:8080/api/v1/upload?storage=cdn" \
  -H "X-API-Key: your-key" \
  -F "file=@image.jpg"

# List files in the "images" bucket
curl -H "X-API-Key: your-key" "http://localhost:8080/api/v1/list?storage=images"
```

When `?storage=` is omitted, the `storage.default` bucket is used.

---

## Multi-Target Backups

Each bucket can have multiple backup targets, each with its own replication mode:

```yaml
storage:
  buckets:
    images:
      type: s3
      bucket: prod-images
      backups:
        - target: "hot-backup"     # sync: fail if backup fails
          mode: sync
        - target: "cold-archive"   # async: best-effort background
          mode: async
        - target: "migration"      # read-fallback: read from here on 404
          mode: read-fallback
```

| Mode | Behavior |
|------|----------|
| `sync` | Writes to primary + backup; fails if backup write fails |
| `async` | Writes to primary, replicates to backup in background |
| `read-fallback` | Writes only to primary; reads fall back to backup on 404 |

Backup targets must reference other buckets defined in the config. A single bucket can have any number of backups mixing different modes and providers (e.g., sync to MinIO + async to R2).

---

## Scoped API Keys

API keys can be scoped at the **bucket**, **group**, or **subgroup** level for multi-tenant isolation:

```yaml
storage:
  buckets:
    client-uploads:
      type: s3
      bucket: client-uploads
      keys:
        - name: "client-a"
          key: "sk-client-a-secret"
        - name: "client-b"
          key: "sk-client-b-secret"

  groups:
    media:
      buckets: ["images", "backups"]
      keys:
        - name: "media-team"
          key: "sk-media-team"
        - name: "readonly-viewer"
          key: "sk-viewer"
          buckets: ["images"]      # restrict to subset of group
      subgroups:
        thumbnails:
          buckets: ["images"]      # subset of parent group
          keys:
            - name: "thumb-service"
              key: "sk-thumb"      # inherits access to "images" only
```

Key resolution:
- **Bucket-level keys** get access to that bucket only
- **Group-level keys** get access to all buckets in the group (or a specified subset)
- **Subgroup-level keys** get access to the subgroup's buckets (which must be a subset of the parent group)

The admin key (`security.api_key`) always has unrestricted access. Requests with a scoped key that try to access an unauthorized bucket receive `403 ACCESS_DENIED`.

---

## File Passthrough

Falco supports uploading and serving any file type — not just images. Non-image files (PDFs, ZIPs, videos, documents, etc.) are stored and served as-is without processing.

```bash
# Upload a PDF
curl -X POST http://localhost:8080/api/v1/upload \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/pdf" \
  --data-binary "@document.pdf"

# Upload any file via multipart
curl -X POST http://localhost:8080/api/v1/upload \
  -H "X-API-Key: your-key" \
  -F "file=@archive.zip"

# Serve it back (no transformation params needed)
curl http://localhost:8080/api/v1/images/abc123
```

Content type is auto-detected. Image transformation query parameters (`?w=`, `?h=`, `?format=`, etc.) are only applied to image files.

---

## Falco vs imgproxy

Both Falco and [imgproxy](https://imgproxy.net) are self-hosted, libvips-based image processing services written in Go. They share a common foundation but have different design philosophies.

### Architecture

| | Falco | imgproxy |
|--|-------|---------|
| **Model** | Storage + processing service | Processing proxy only |
| **Image storage** | Built-in (local FS, S3, MinIO, R2) with bucket groups and multi-target backups | None — processes remote URLs on-the-fly |
| **Image upload** | Yes — REST API with deduplication | No |
| **Image management** | List, delete, directory organization | No |
| **Web dashboard** | Yes — HTMX + Tailwind UI | No (Pro plan only) |
| **Processing engine** | libvips via govips | libvips |
| **Language** | Go | Go |

### Image Transformations

| Feature | Falco | imgproxy |
|---------|-------|---------|
| Resize (cover/contain/fill) | ✅ | ✅ |
| Smart/entropy crop | ✅ | ✅ |
| Gravity-aware crop | ✅ | ✅ |
| Manual crop | ✅ | ✅ |
| Rotate / Flip | ✅ | ✅ |
| Brightness/Contrast/Gamma | ✅ | ✅ |
| Saturation/Hue | ✅ | ✅ |
| Blur / Sharpen | ✅ | ✅ |
| Trim (remove borders) | ✅ | ✅ |
| Padding with color | ✅ | ✅ |
| Watermark (URL, opacity, position, scale) | ✅ | ✅ (Pro: advanced) |
| Auto-orient from EXIF | ✅ | ✅ |
| Strip metadata | ✅ | ✅ |
| AVIF output | ✅ (WebP fallback) | ✅ |
| GIF support | ✅ (static) | ✅ (animated, Pro) |
| SVG input | ✅ | ✅ |
| PDF processing | ❌ | ✅ |
| Video thumbnails | ❌ | ✅ (Pro) |
| Chained pipelines | ❌ | ✅ (Pro) |

### Security

| Feature | Falco | imgproxy |
|---------|-------|---------|
| HMAC URL signing | ✅ | ✅ |
| Signature required mode | ✅ | ✅ |
| API key auth for writes | ✅ | N/A (read-only) |
| SSRF protection | ✅ | ✅ |
| Trusted proxies | ✅ | ✅ |
| Rate limiting | ✅ | ✅ (Pro) |
| Path traversal protection | ✅ | N/A |

### Observability

| Feature | Falco | imgproxy |
|---------|-------|---------|
| Prometheus metrics | ✅ | ✅ |
| Cache metrics (size, evictions) | ✅ | ✅ |
| Structured JSON logging | ✅ (zerolog) | ✅ |
| OpenAPI docs | ✅ | ❌ |
| pprof | ✅ | ✅ |

### Caching

| Feature | Falco | imgproxy |
|---------|-------|---------|
| In-memory LRU cache | ✅ | ✅ |
| Redis cache | ✅ | ✅ (Pro) |
| `Cache-Control` / `s-maxage` config | ✅ | ✅ |

### Licensing & Cost

| | Falco | imgproxy |
|-|-------|---------|
| License | GPLv3 | MIT (OSS) / Commercial (Pro) |
| Advanced features | Included | Some features require Pro ($) |

---

### When to use Falco

- You need **full image lifecycle management** — upload, store, organize, delete — not just transformation
- You want a **single service** that replaces both object storage API wrappers and an image CDN proxy
- You're building a **multi-tenant** app and need bucket/directory isolation
- You want a **web dashboard** to browse and manage stored images without external tooling
- You need **content deduplication** to avoid storing the same image twice
- You're self-hosting and prefer **GPLv3** open-source software

### When to use imgproxy

- You already store images in S3/GCS/any URL-accessible storage and only need **on-the-fly transformations**
- You need **animated GIF processing**, video thumbnails, or PDF support
- You want a **battle-tested, widely deployed** solution with commercial support options
- Your team is already familiar with the imgproxy URL format and ecosystem
- You need the most comprehensive set of transformations including advanced Pro features

---

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
