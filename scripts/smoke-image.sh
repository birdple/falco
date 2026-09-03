#!/usr/bin/env bash
#
# Arranca una imagen de falco y exige que /health conteste con la versión que se
# le inyectó al compilar.
#
#   scripts/smoke-image.sh falco:ci 0.0.0-ci
#
# Una imagen que se publica sin haber arrancado nunca es una promesa rota: el
# release la anuncia y el primer `docker pull` descubre que no levanta.
set -euo pipefail

IMAGE="${1:?falta la imagen}"
EXPECTED="${2:?falta la versión esperada}"
PORT="${SMOKE_PORT:-18081}"
NAME="falco-smoke-$$"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$NAME" -p "${PORT}:8080" \
  -e PORT=8080 \
  -e STORAGE_DEFAULT=local \
  -e STORAGE_BUCKET_LOCAL_TYPE=filesystem \
  -e STORAGE_BUCKET_LOCAL_PATH=/app/data/images \
  -e API_KEY_REQUIRED=false \
  -e HMAC_REQUIRED=false \
  -e LOG_FORMAT=json \
  "$IMAGE" >/dev/null

ready=0
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done

docker logs "$NAME"

if [ "$ready" -ne 1 ]; then
  echo "la imagen nunca respondió en /health" >&2
  exit 1
fi

BODY="$(curl -fsS "http://127.0.0.1:${PORT}/health")"
if ! printf '%s' "$BODY" | grep -q "\"version\":\"${EXPECTED}\""; then
  echo "la imagen reporta otra versión: se esperaba ${EXPECTED}" >&2
  echo "$BODY" >&2
  exit 1
fi

echo "==> smoke ok: $BODY"
