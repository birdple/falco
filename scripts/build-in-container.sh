#!/usr/bin/env bash
#
# Runs scripts/release-binary.sh inside the container that brings the
# requested libc, for the current machine's architecture.
#
#   scripts/build-in-container.sh glibc 0.13.0 abc1234 dist
#   scripts/build-in-container.sh musl  0.13.0 abc1234 dist
#
# It does not cross-compile: an amd64 runner produces an amd64 binary and an
# arm64 one an arm64 binary. The architecture matrix is made of runners, not
# GOARCH, because CGO against a cross libvips is exactly the kind of build that
# compiles and does not start.
#
# The Go version comes from go.mod so there is no second copy of the number to
# fall behind.
set -euo pipefail

LIBC="${1:?missing libc: glibc | musl}"
VERSION="${2:-0.0.0-dev}"
COMMIT="${3:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
DIST="${4:-dist}"

GO_VERSION="$(awk '/^go /{print $2; exit}' go.mod)"
mkdir -p "$DIST"

case "$LIBC" in
  musl)
    # Alpine 3.24 ships vips 8.18.2 in community. It is the same base as the
    # Dockerfile, so this binary and the image's are the same build.
    docker run --rm \
      -v "$PWD":/src -w /src \
      "golang:${GO_VERSION}-alpine3.24" \
      sh -euc "
        apk add --no-cache gcc g++ musl-dev vips-dev pkgconf curl bash tar >/dev/null
        bash scripts/release-binary.sh '${VERSION}' '${COMMIT}' '${DIST}'
      "
    ;;

  glibc)
    # Ubuntu 26.04 is the first LTS with libvips 8.18 in apt; 24.04 stops at
    # 8.15 and does not even compile. The distro's Go may lag behind, so the
    # exact toolchain from go.mod is downloaded, and checked against its
    # published SHA-256 before it is used.
    ARCH="$(docker version --format '{{.Server.Arch}}')"
    docker run --rm \
      -v "$PWD":/src -w /src \
      ubuntu:26.04 \
      bash -euc "
        apt-get update -qq >/dev/null
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
          build-essential pkg-config libvips-dev curl ca-certificates tar >/dev/null
        cd /tmp
        go_tgz='go${GO_VERSION}.linux-${ARCH}.tar.gz'
        curl -fsSLO \"https://dl.google.com/go/\${go_tgz}\"
        echo \"\$(curl -fsSL https://dl.google.com/go/\${go_tgz}.sha256)  \${go_tgz}\" | sha256sum -c -
        tar -C /usr/local -xzf \"\${go_tgz}\"
        cd /src
        export PATH=/usr/local/go/bin:\$PATH
        bash scripts/release-binary.sh '${VERSION}' '${COMMIT}' '${DIST}'
      "
    ;;

  *)
    echo "unknown libc: ${LIBC} (use glibc or musl)" >&2
    exit 1
    ;;
esac
