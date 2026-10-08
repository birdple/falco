#!/usr/bin/env bash
#
# Starts a falco image and requires /health to answer with the version injected
# at build time.
#
#   scripts/smoke-image.sh falco:ci 0.0.0-ci
#
# An image published without ever being started is a broken promise: the
# release announces it and the first `docker pull` finds out it does not come up.
set -euo pipefail

IMAGE="${1:?missing image}"
EXPECTED="${2:?missing expected version}"
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
  echo "the image never answered on /health" >&2
  exit 1
fi

BODY="$(curl -fsS "http://127.0.0.1:${PORT}/health")"
if ! printf '%s' "$BODY" | grep -q "\"version\":\"${EXPECTED}\""; then
  echo "the image reports another version: expected ${EXPECTED}" >&2
  echo "$BODY" >&2
  exit 1
fi

echo "==> smoke ok: $BODY"
