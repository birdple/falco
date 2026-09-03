---
title: Install
description: Container image, prebuilt binaries and building from source — and the libvips requirement behind all three.
---

Falco links libvips through cgo. That single fact decides how you install it: the
container image carries its own libvips, the prebuilt binaries expect to find one
on the host, and building from source needs the development headers.

## Container image

The image is built and **started** in CI before it is pushed, on every tag, for
`linux/amd64` and `linux/arm64`.

```bash
docker pull ghcr.io/birdple/falco:latest
```

Pin a version in anything that matters:

```bash
docker pull ghcr.io/birdple/falco:0.13.0   # exact
docker pull ghcr.io/birdple/falco:0.13     # latest patch of that minor
```

It runs as an unprivileged user (uid 1001), keeps data under `/app/data`, and
listens on `$PORT` (8080 when unset). See [Deploying Falco](/falco/guides/deployment/)
for a compose file worth copying.

## Prebuilt binaries

Every release attaches five archives. They are **not static**: each one needs
libvips 8.18 present on the host, and the two Linux builds are not
interchangeable — one links against glibc's libvips and the other against musl's.

| Archive | For |
|---|---|
| `falco_<version>_linux_amd64.tar.gz` | Linux glibc (Debian, Ubuntu, Fedora…), x86-64 |
| `falco_<version>_linux_arm64.tar.gz` | Linux glibc, ARM64 |
| `falco_<version>_linux_amd64_musl.tar.gz` | Alpine and other musl distributions, x86-64 |
| `falco_<version>_linux_arm64_musl.tar.gz` | Alpine, ARM64 |
| `falco_<version>_darwin_arm64.tar.gz` | macOS, Apple Silicon |

Install libvips first:

```bash
sudo apt install libvips42t64   # Debian/Ubuntu 26.04 or newer
apk add vips                 # Alpine
brew install vips            # macOS
```

Then:

```bash
tar xzf falco_0.13.0_linux_amd64.tar.gz
cd falco_0.13.0_linux_amd64
./falco-server
```

Every archive was compiled **and started** on its own platform before being
published — a cgo binary that compiles is not yet a cgo binary that links at
runtime, so the release does not take that on faith. Checksums are in
`checksums.txt` on the release page.

:::caution[Why the libvips version is exact]
Falco uses `github.com/cshum/vipsgen/vips`, whose bindings are generated against
libvips **8.18**. An older libvips does not merely warn — the package does not
compile, and a binary built against one ABI will not load another. Ubuntu 24.04
ships 8.15 and Debian trixie 8.16; neither works.
:::

## From source

```bash
git clone https://github.com/birdple/falco.git
cd falco

# Development headers, not just the runtime library
sudo apt install libvips-dev   # Debian/Ubuntu 26.04+
apk add vips-dev               # Alpine
brew install vips              # macOS ships both

go build ./cmd/server
```

Go 1.27 or newer, and `CGO_ENABLED=1` (the default, unless you have turned it off
globally).

To reproduce exactly what a release publishes — build, smoke test and archive —
run the script the release itself runs:

```bash
scripts/release-binary.sh 0.13.0 "$(git rev-parse --short HEAD)" dist
```

Or build in the container that provides the libc you want, without installing
libvips on your machine at all:

```bash
scripts/build-in-container.sh musl 0.13.0 "$(git rev-parse --short HEAD)" dist
```
