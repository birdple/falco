#!/usr/bin/env bash
#
# Builds, tests and packages ONE falco binary for the platform it runs on. It
# does not cross-compile: falco links libvips through CGO, so the only build to
# trust is the native one, against the libvips of the system it runs on.
#
# The release runs this same script five times (glibc/musl × amd64/arm64, plus
# darwin/arm64), each inside the container or runner that brings its libc.
# Running it by hand produces exactly the tar.gz CI publishes:
#
#   scripts/release-binary.sh 0.13.0 abc1234 dist
#
# The smoke test is not optional: a CGO binary can compile and still fail to
# link at runtime. Publishing a tar.gz nobody started is publishing nothing.
set -euo pipefail

VERSION="${1:-0.0.0-dev}"
COMMIT="${2:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
DIST_ARG="${3:-dist}"

MODULE="github.com/birdple/falco"
BINARY="falco-server"

ROOT="$(pwd)"
mkdir -p "$DIST_ARG"
DIST="$(cd "$DIST_ARG" && pwd)"

GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"

# The suffix tells apart the two Linux binaries that are NOT interchangeable:
# one links against glibc's libvips and the other against musl's. Without it
# they would share a file name and an Alpine user would get the one that does
# not start.
LIBC=""
if [ "$GOOS" = "linux" ] && ls /lib/ld-musl-*.so.1 >/dev/null 2>&1; then
  LIBC="_musl"
fi

NAME="falco_${VERSION}_${GOOS}_${GOARCH}${LIBC}"
STAGE="${DIST}/${NAME}"

# vipsgen 1.3 generates bindings against libvips 8.18: with an older one the
# package does not even compile. It is checked here so the failure says why,
# instead of surfacing as a hundred C errors with no context.
VIPS_VERSION="$(pkg-config --modversion vips)"
echo "==> libvips ${VIPS_VERSION} — target ${NAME}"
case "$VIPS_VERSION" in
  8.18.*) ;;
  *)
    echo "libvips ${VIPS_VERSION} will not do: github.com/cshum/vipsgen/vips needs 8.18.x" >&2
    exit 1
    ;;
esac

rm -rf "$STAGE"
mkdir -p "$STAGE"

# netgo/osusergo keep the binary off the system's NSS, so libvips stays its only
# dynamic dependency. They do not apply on macOS: there the CGO resolver is the
# system's, and forcing them only breaks the build.
TAGS=""
if [ "$GOOS" = "linux" ]; then
  TAGS="netgo osusergo"
fi

CGO_ENABLED=1 go build \
  ${TAGS:+-tags "$TAGS"} \
  -trimpath \
  -ldflags "-s -w -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT}" \
  -o "${STAGE}/${BINARY}" \
  ./cmd/server

# ---------------------------------------------------------------------------
# Smoke test: actually start it and require /health to report this version.
#
# It starts FROM an empty directory, not from the repo: viper reads config.yaml
# from the working directory, and the repo's declares a jay bucket that needs
# credentials. Whoever unpacks the tar.gz will not have it either, so this
# tests the binary as they will receive it.
# ---------------------------------------------------------------------------
SMOKE_PORT="${SMOKE_PORT:-18080}"
SMOKE_DIR="$(mktemp -d)"
LOG="${SMOKE_DIR}/falco.log"

cleanup() {
  if [ -n "${PID:-}" ]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  cd "$ROOT"
  rm -rf "$SMOKE_DIR"
}
trap cleanup EXIT

cd "$SMOKE_DIR"

# The minimum configuration falco accepts to start with: a filesystem bucket and
# the two security flags, which on purpose have no default.
PORT="$SMOKE_PORT" \
HOST=127.0.0.1 \
STORAGE_DEFAULT=local \
STORAGE_BUCKET_LOCAL_TYPE=filesystem \
STORAGE_BUCKET_LOCAL_PATH="${SMOKE_DIR}/images" \
API_KEY_REQUIRED=false \
HMAC_REQUIRED=false \
LOG_FORMAT=json \
  "${STAGE}/${BINARY}" >"$LOG" 2>&1 &
PID=$!

ready=0
for _ in $(seq 1 30); do
  if ! kill -0 "$PID" 2>/dev/null; then
    break
  fi
  if curl -fsS "http://127.0.0.1:${SMOKE_PORT}/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done

if [ "$ready" -ne 1 ]; then
  echo "falco never answered on /health" >&2
  cat "$LOG" >&2
  exit 1
fi

BODY="$(curl -fsS "http://127.0.0.1:${SMOKE_PORT}/health")"
if ! printf '%s' "$BODY" | grep -q "\"version\":\"${VERSION}\""; then
  echo "the binary reports another version: expected ${VERSION}" >&2
  echo "$BODY" >&2
  exit 1
fi
echo "==> smoke ok: ${BODY}"

cd "$ROOT"

# ---------------------------------------------------------------------------
# Packaging
# ---------------------------------------------------------------------------
cp README.md LICENSE config.yaml .env.example "$STAGE/"

tar -czf "${DIST}/${NAME}.tar.gz" -C "$DIST" "$NAME"
rm -rf "$STAGE"

echo "==> ${DIST}/${NAME}.tar.gz"
