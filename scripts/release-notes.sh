#!/usr/bin/env bash
#
# Writes the GitHub release body for a tag.
#
#   scripts/release-notes.sh v0.13.0 > notes.md
#
# Needs the full history (fetch-depth: 0): with a shallow clone there is no
# previous tag to compare against, and the change list would come out empty
# without saying why.
set -euo pipefail

TAG="${1:?missing tag, for example v0.13.0}"
VERSION="${TAG#v}"

PREVIOUS="$(git describe --tags --abbrev=0 "${TAG}^" 2>/dev/null || true)"

echo "## Changes"
echo
if [ -z "$PREVIOUS" ]; then
  echo "First tagged release of the repository."
else
  # The same filters jay uses: docs and dependency bumps are not news to
  # someone reading a release.
  git log --no-merges --pretty=format:"- %s (%h)" "${PREVIOUS}..${TAG}" \
    | grep -v -E "^- (docs|test|chore\(deps\)):" \
    || echo "- Nothing to report since ${PREVIOUS}."
  echo
  echo
  echo "**Full comparison:** \`${PREVIOUS}...${TAG}\`"
fi

cat <<NOTES

## Container image

\`\`\`bash
docker pull ghcr.io/birdple/falco:${VERSION}
\`\`\`

## Binaries

falco links **libvips through CGO**, so the binaries are not static: each one
needs libvips **8.18.x** installed on the system it runs on. Pick by your
distribution's libc, not only by architecture.

| File | For |
|---|---|
| \`falco_${VERSION}_linux_amd64.tar.gz\` | Linux glibc (Debian, Ubuntu, Fedora…), x86-64 |
| \`falco_${VERSION}_linux_arm64.tar.gz\` | Linux glibc, ARM64 |
| \`falco_${VERSION}_linux_amd64_musl.tar.gz\` | Alpine and other musl, x86-64 |
| \`falco_${VERSION}_linux_arm64_musl.tar.gz\` | Alpine and other musl, ARM64 |
| \`falco_${VERSION}_darwin_arm64.tar.gz\` | macOS, Apple Silicon |

\`\`\`bash
# Debian/Ubuntu 26.04+ (the runtime; the headers are libvips-dev)
sudo apt install libvips42t64

# Alpine
apk add vips

# macOS
brew install vips
\`\`\`

Every file was built **and started** on its platform before it was published.
The checksums are in \`checksums.txt\`.
NOTES
