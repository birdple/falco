#!/usr/bin/env bash
#
# Corre golangci-lint sobre Linux, que es donde lo corre CI.
#
#   scripts/lint-in-container.sh
#
# Existe porque `make lint` en macOS NO ve lo mismo: hay linters cuyo resultado
# depende de la plataforma. El caso que lo motivó es `unconvert` sobre
# syscall.Statfs_t.Bsize, que es int64 en Linux y uint32 en Darwin — la
# conversión sobra en una y es obligatoria en la otra, así que el lint pasaba en
# local y fallaba en CI.
#
# La versión de Go sale de go.mod y la de golangci-lint de acá abajo, que es la
# que resuelve `version: latest` en el workflow. Si CI empieza a fallar por una
# regla nueva, subir este número reproduce el fallo en local.
set -euo pipefail

GOLANGCI_VERSION="${GOLANGCI_VERSION:-2.13.2}"
GO_VERSION="$(awk '/^go /{print $2; exit}' go.mod)"
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
    curl -fsSL 'https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_VERSION}/golangci-lint-${GOLANGCI_VERSION}-linux-${ARCH}.tar.gz' \
      | tar -xz -C /tmp
    '/tmp/golangci-lint-${GOLANGCI_VERSION}-linux-${ARCH}/golangci-lint' run
  "
