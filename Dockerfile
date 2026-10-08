# ----------------------------------------
# Build stage
# ----------------------------------------
# Alpine 3.24 is pinned explicitly (not the Go image's floating "alpine") so the
# builder and the runtime are always the same Alpine release, and both images
# are pinned by digest so a rebuild of the same commit uses the same bases.
# Dependabot proposes digest bumps.
FROM golang:1.27-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

# Build dependencies for CGO against libvips:
# - gcc, g++, musl-dev: the C/C++ toolchain CGO needs
# - vips-dev: libvips and its headers; github.com/cshum/vipsgen needs 8.18.x,
#   which Alpine 3.24 ships in community (enabled by default)
# - pkgconf: pkg-config, to locate the libraries
RUN apk add --no-cache \
        gcc \
        g++ \
        musl-dev \
        vips-dev \
        pkgconf

WORKDIR /app

# Modules first, so the download layer is cached across source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Version and commit reported by /health. Empty by default: without them the
# binary keeps the value compiled into internal/version instead of reporting
# "dev" and making a local build lie about what it is.
ARG VERSION=""
ARG COMMIT=""

# Build tags:
#   - netgo: Go's own DNS resolver (portable on Alpine)
#   - osusergo: Go's own user/group lookups (portable)
# Flags:
#   - -ldflags "-w -s": no debug info or symbol table (about 40% smaller)
#   - -trimpath: no absolute builder paths in the binary
# A static build is not possible: vipsgen needs CGO and a dynamic libvips.
RUN set -eux; \
    LDFLAGS="-w -s"; \
    if [ -n "$VERSION" ]; then \
      LDFLAGS="$LDFLAGS -X github.com/birdple/falco/internal/version.Version=$VERSION"; \
    fi; \
    if [ -n "$COMMIT" ]; then \
      LDFLAGS="$LDFLAGS -X github.com/birdple/falco/internal/version.Commit=$COMMIT"; \
    fi; \
    CGO_ENABLED=1 go build \
      -tags 'netgo osusergo' \
      -trimpath \
      -ldflags="$LDFLAGS" \
      -o falco-server \
      ./cmd/server

# ----------------------------------------
# Runtime stage
# ----------------------------------------
# Same Alpine release as the builder, so the runtime libvips matches (ABI) the
# vips-dev the binary was compiled against.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

# Runtime dependencies:
# - ca-certificates: TLS roots for jay/S3 and the proxy's outbound fetches
# - tzdata: time zones
# - vips: libvips, runtime only
# - wget: GNU wget for the healthcheck (busybox's lacks --no-verbose)
RUN apk add --no-cache \
        ca-certificates \
        tzdata \
        vips \
        wget && \
    addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup && \
    mkdir -p /app/data /app/logs && \
    chown -R appuser:appgroup /app

WORKDIR /app

# The API description (docs/openapi.yaml) and the panel assets (web/static) are
# NOT copied: they travel INSIDE the binary through go:embed (docs/embed.go,
# web/embed.go).
COPY --from=builder --chown=appuser:appgroup /app/falco-server .

# libvips refuses the loaders it marks as untrusted (ImageMagick, PDF, matrix
# and friends). falco sets this itself when it is unset; it is stated here too
# so the image's policy is visible without reading the code.
ENV VIPS_BLOCK_UNTRUSTED=1

USER appuser

# The port birdple-v2 deploys on (the root compose injects PORT=4009). The
# binary's own default is still 8080, which is why the healthcheck falls back
# to 8080 when PORT is unset.
EXPOSE 4009

# Uses $PORT to follow the real deploy port (PaaS platforms often inject one).
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider "http://localhost:${PORT:-8080}/health" || exit 1

CMD ["./falco-server"]
