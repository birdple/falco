#!/usr/bin/env bash
#
# Compila, prueba y empaqueta UN binario de falco para la plataforma en la que
# corre. No cross-compila: falco enlaza libvips por CGO, así que el único build
# confiable es el nativo, con la libvips del sistema donde se ejecuta.
#
# El release corre este mismo script cinco veces (glibc/musl × amd64/arm64, más
# darwin/arm64), cada una dentro del contenedor o runner que aporta su libc.
# Correrlo a mano produce exactamente el mismo tar.gz que publica CI:
#
#   scripts/release-binary.sh 0.13.0 abc1234 dist
#
# El smoke test no es opcional: un binario CGO puede compilar y no enlazar en
# tiempo de ejecución. Publicar un tar.gz que nadie arrancó es publicar nada.
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

# El sufijo distingue los dos binarios de Linux que NO son intercambiables: uno
# enlaza contra la libvips de glibc y el otro contra la de musl. Sin el sufijo
# serían el mismo nombre de archivo y quien use Alpine se llevaría el que no
# arranca.
LIBC=""
if [ "$GOOS" = "linux" ] && ls /lib/ld-musl-*.so.1 >/dev/null 2>&1; then
  LIBC="_musl"
fi

NAME="falco_${VERSION}_${GOOS}_${GOARCH}${LIBC}"
STAGE="${DIST}/${NAME}"

# vipsgen 1.3 genera bindings contra libvips 8.18: con una anterior el paquete
# ni siquiera compila. Se comprueba acá para que el fallo diga por qué, en vez
# de salir como cien errores de C sin contexto.
VIPS_VERSION="$(pkg-config --modversion vips)"
echo "==> libvips ${VIPS_VERSION} — destino ${NAME}"
case "$VIPS_VERSION" in
  8.18.*) ;;
  *)
    echo "libvips ${VIPS_VERSION} no sirve: github.com/cshum/vipsgen/vips exige 8.18.x" >&2
    exit 1
    ;;
esac

rm -rf "$STAGE"
mkdir -p "$STAGE"

# netgo/osusergo evitan que el binario dependa del NSS del sistema; así libvips
# queda como su única dependencia dinámica. En macOS no aplican: ahí el
# resolver de CGO es el del sistema y forzarlos sólo rompe el build.
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
# Smoke test: arrancar de verdad y exigir que /health reporte esta versión.
#
# Se arranca DESDE un directorio vacío y no desde el repo: viper toma el
# config.yaml del directorio de trabajo, y el del repo declara un bucket jay que
# exige credenciales. Quien descomprima el tar.gz tampoco lo va a tener, así que
# esto prueba el binario tal como lo va a recibir.
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

# Configuración mínima con la que falco acepta arrancar: un bucket de sistema de
# archivos y las dos banderas de seguridad, que a propósito no tienen default.
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
  echo "falco nunca respondió en /health" >&2
  cat "$LOG" >&2
  exit 1
fi

BODY="$(curl -fsS "http://127.0.0.1:${SMOKE_PORT}/health")"
if ! printf '%s' "$BODY" | grep -q "\"version\":\"${VERSION}\""; then
  echo "el binario reporta otra versión: se esperaba ${VERSION}" >&2
  echo "$BODY" >&2
  exit 1
fi
echo "==> smoke ok: ${BODY}"

cd "$ROOT"

# ---------------------------------------------------------------------------
# Empaquetado
# ---------------------------------------------------------------------------
cp README.md LICENSE config.yaml .env.example "$STAGE/"

tar -czf "${DIST}/${NAME}.tar.gz" -C "$DIST" "$NAME"
rm -rf "$STAGE"

echo "==> ${DIST}/${NAME}.tar.gz"
