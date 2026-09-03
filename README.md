# Falco

A self-hosted image service in Go. Upload an image once, then ask for any size,
crop or format from the URL. libvips does the pixels; the filesystem, S3,
Cloudflare R2 or [Jay](https://github.com/ivangsm/jay) holds the bytes.

One process. No database, no message broker, nothing to migrate.

**📖 [Full documentation](https://birdple.github.io/falco/)**

```bash
docker run -p 8080:8080 -v falco-data:/app/data \
  -e STORAGE_DEFAULT=local \
  -e STORAGE_BUCKET_LOCAL_TYPE=filesystem \
  -e STORAGE_BUCKET_LOCAL_PATH=/app/data/images \
  -e API_KEY_REQUIRED=false \
  -e HMAC_REQUIRED=false \
  ghcr.io/birdple/falco:latest
```

```bash
# Upload
curl -X POST localhost:8080/api/v1/upload -F "file=@photo.jpg"
# → {"success":true,"data":{"id":"6d556268ff5afc0f", …}}

# Every size is a URL. Nothing is pre-generated.
curl -o thumb.webp "localhost:8080/api/v1/images/6d556268ff5afc0f?w=400"
curl -o square.webp "localhost:8080/api/v1/images/6d556268ff5afc0f?w=300&h=300&gravity=smart"
curl -o cover.avif "localhost:8080/api/v1/images/6d556268ff5afc0f.avif?w=1200"
```

## What it does

- **Stores and transforms.** Upload, deduplicate by content hash, organise into
  buckets and directories, list, delete — and resize, crop, rotate, adjust
  colour, watermark and re-encode on the way out, cached in memory.
- **Four storage backends,** all the same kind of thing: filesystem, S3 (and
  anything S3-compatible), R2, and Jay over its native protocol.
- **Multi-target backups** per bucket, in `sync`, `async` or `read-fallback`
  mode.
- **Signed delivery URLs.** HMAC-SHA256 over path *and* query, so nobody mints
  transformations on your CPU.
- **Scoped API keys** at bucket, group or subgroup level, enforced on upload,
  list, delete, delivery and signing.
- **An admin panel,** server-rendered, included.
- **Prometheus metrics, structured logs, OpenTelemetry traces and pprof.**

For what it deliberately does *not* do — and how it compares to imgproxy — see
[What Falco is](https://birdple.github.io/falco/what-falco-is/).

## Requirements

libvips **8.18**, because the bindings (`github.com/cshum/vipsgen/vips`) are
generated against it. Ubuntu 24.04 ships 8.15 and Debian trixie 8.16; neither
works. The container image already has the right one.

```bash
sudo apt install libvips-dev   # Debian/Ubuntu 26.04+
apk add vips-dev               # Alpine
brew install vips              # macOS
```

## Install

| | |
|---|---|
| Container | `ghcr.io/birdple/falco:latest` (`linux/amd64`, `linux/arm64`) |
| Binaries | Five archives per [release](https://github.com/birdple/falco/releases): Linux glibc and musl on amd64 and arm64, plus macOS arm64 |
| Source | `go build ./cmd/server` with Go 1.27+ and `CGO_ENABLED=1` |

The binaries are **not static** — each needs libvips on the host, and the glibc
and musl builds are not interchangeable. Every one of them was compiled *and
started* on its own platform before being published.
[Install](https://birdple.github.io/falco/install/) has the details.

## Configuration

Three layers, last one wins: compiled defaults, then `config.yaml`, then
environment variables.

Four settings have **no default on purpose**: without a bucket Falco refuses to
start, `API_KEY_REQUIRED` and `HMAC_REQUIRED` must be stated explicitly, and an
unset `HMAC_REQUIRE_EXPIRY` makes delivery answer 500 rather than quietly
accepting signatures that never expire.

Every variable is listed in
[Configuration](https://birdple.github.io/falco/reference/configuration/).

## Development

```bash
make check        # fmt + vet + lint + test + build — what has to pass before a commit
make test
go run ./cmd/server
```

Reproduce exactly what a release publishes, including the smoke test:

```bash
scripts/release-binary.sh 0.13.0 "$(git rev-parse --short HEAD)" dist
scripts/build-in-container.sh musl 0.13.0 "$(git rev-parse --short HEAD)" dist
```

The documentation site lives in [`site/`](site/) and is the single source of
truth for how Falco behaves:

```bash
cd site && bun install && bun run dev
```

## License

[GNU General Public License v3.0](LICENSE).
