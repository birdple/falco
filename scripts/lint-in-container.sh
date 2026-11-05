#!/usr/bin/env bash
#
# Runs golangci-lint on Linux, which is where CI runs it.
#
#   scripts/lint-in-container.sh
#
# It exists because `make lint` on macOS does NOT see the same thing: some
# linters depend on the platform. The case that prompted it is `unconvert` on
# syscall.Statfs_t.Bsize, an int64 on Linux and a uint32 on Darwin — the
# conversion is redundant on one and required on the other, so lint passed
# locally and failed in CI.
#
# The Go version comes from go.mod; the golangci-lint version below must match
# GOLANGCI_LINT_VERSION in .github/workflows/ci.yml and the Makefile. Both
# downloads are checked against their published SHA-256 before they run.
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

    cd /tmp
    go_tgz='go${GO_VERSION}.linux-${ARCH}.tar.gz'
    curl -fsSLO \"https://dl.google.com/go/\${go_tgz}\"
    echo \"\$(curl -fsSL https://dl.google.com/go/\${go_tgz}.sha256)  \${go_tgz}\" | sha256sum -c -
    tar -C /usr/local -xzf \"\${go_tgz}\"
    export PATH=/usr/local/go/bin:\$PATH

    lint_tgz='golangci-lint-${GOLANGCI_VERSION}-linux-${ARCH}.tar.gz'
    base='https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_VERSION}'
    curl -fsSLO \"\${base}/\${lint_tgz}\"
    curl -fsSL \"\${base}/golangci-lint-${GOLANGCI_VERSION}-checksums.txt\" | grep \" \${lint_tgz}\$\" | sha256sum -c -
    tar -xzf \"\${lint_tgz}\"

    cd /src
    '/tmp/golangci-lint-${GOLANGCI_VERSION}-linux-${ARCH}/golangci-lint' run
  "
