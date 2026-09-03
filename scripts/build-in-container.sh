#!/usr/bin/env bash
#
# Corre scripts/release-binary.sh dentro del contenedor que aporta la libc
# pedida, para la arquitectura de la máquina actual.
#
#   scripts/build-in-container.sh glibc 0.13.0 abc1234 dist
#   scripts/build-in-container.sh musl  0.13.0 abc1234 dist
#
# No cross-compila: en un runner amd64 sale un binario amd64 y en uno arm64 uno
# arm64. La matriz de arquitecturas se arma con runners, no con GOARCH, porque
# CGO contra libvips cruzada es exactamente la clase de build que compila y no
# arranca.
#
# La versión de Go sale de go.mod para que no exista una segunda copia del
# número que se pueda quedar atrás.
set -euo pipefail

LIBC="${1:?falta la libc: glibc | musl}"
VERSION="${2:-0.0.0-dev}"
COMMIT="${3:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
DIST="${4:-dist}"

GO_VERSION="$(awk '/^go /{print $2; exit}' go.mod)"
mkdir -p "$DIST"

case "$LIBC" in
  musl)
    # Alpine 3.24 trae vips 8.18.2 en community. Es la misma base que el
    # Dockerfile, así que este binario y el de la imagen son el mismo build.
    docker run --rm \
      -v "$PWD":/src -w /src \
      "golang:${GO_VERSION}-alpine3.24" \
      sh -euc "
        apk add --no-cache gcc g++ musl-dev vips-dev pkgconf curl bash tar >/dev/null
        bash scripts/release-binary.sh '${VERSION}' '${COMMIT}' '${DIST}'
      "
    ;;

  glibc)
    # Ubuntu 26.04 es la primera LTS con libvips 8.18 en apt; 24.04 se queda en
    # 8.15 y ni compila. El Go de la distro puede ir atrasado, así que se baja
    # el toolchain exacto de go.mod.
    ARCH="$(docker version --format '{{.Server.Arch}}')"
    docker run --rm \
      -v "$PWD":/src -w /src \
      ubuntu:26.04 \
      bash -euc "
        apt-get update -qq >/dev/null
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
          build-essential pkg-config libvips-dev curl ca-certificates tar >/dev/null
        curl -fsSL 'https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz' | tar -C /usr/local -xz
        export PATH=/usr/local/go/bin:\$PATH
        bash scripts/release-binary.sh '${VERSION}' '${COMMIT}' '${DIST}'
      "
    ;;

  *)
    echo "libc desconocida: ${LIBC} (usa glibc o musl)" >&2
    exit 1
    ;;
esac
