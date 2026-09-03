#!/usr/bin/env bash
#
# Arma el cuerpo del release de GitHub para un tag.
#
#   scripts/release-notes.sh v0.13.0 > notes.md
#
# Necesita el historial completo (fetch-depth: 0): con un clon superficial no
# hay tag anterior con el que comparar y la lista de cambios saldría vacía sin
# decir por qué.
set -euo pipefail

TAG="${1:?falta el tag, por ejemplo v0.13.0}"
VERSION="${TAG#v}"

PREVIOUS="$(git describe --tags --abbrev=0 "${TAG}^" 2>/dev/null || true)"

echo "## Cambios"
echo
if [ -z "$PREVIOUS" ]; then
  echo "Primer release etiquetado del repositorio."
else
  # Los mismos filtros que usa jay: la documentación y los bumps de dependencias
  # no son noticia para quien lee un release.
  git log --no-merges --pretty=format:"- %s (%h)" "${PREVIOUS}..${TAG}" \
    | grep -v -E "^- (docs|test|chore\(deps\)):" \
    || echo "- Sin cambios que reportar desde ${PREVIOUS}."
  echo
  echo
  echo "**Comparación completa:** \`${PREVIOUS}...${TAG}\`"
fi

cat <<EOF

## Imagen de contenedor

\`\`\`bash
docker pull ghcr.io/birdple/falco:${VERSION}
\`\`\`

## Binarios

falco enlaza **libvips por CGO**, así que los binarios no son estáticos: cada
uno necesita libvips **8.18.x** instalada en el sistema donde corre. Elige por
la libc de tu distribución, no sólo por la arquitectura.

| Archivo | Para |
|---|---|
| \`falco_${VERSION}_linux_amd64.tar.gz\` | Linux glibc (Debian, Ubuntu, Fedora…), x86-64 |
| \`falco_${VERSION}_linux_arm64.tar.gz\` | Linux glibc, ARM64 |
| \`falco_${VERSION}_linux_amd64_musl.tar.gz\` | Alpine y otras musl, x86-64 |
| \`falco_${VERSION}_linux_arm64_musl.tar.gz\` | Alpine y otras musl, ARM64 |
| \`falco_${VERSION}_darwin_arm64.tar.gz\` | macOS, Apple Silicon |

\`\`\`bash
# Debian/Ubuntu 26.04+ (el runtime; los headers son libvips-dev)
sudo apt install libvips42t64

# Alpine
apk add vips

# macOS
brew install vips
\`\`\`

Cada archivo se compiló y **se arrancó** en su plataforma antes de publicarse.
Los checksums están en \`checksums.txt\`.
EOF
