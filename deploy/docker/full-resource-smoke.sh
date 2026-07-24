#!/bin/sh
set -eu

IMAGE=${1:-kiln:local}
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
CONFIG=${KILN_FULL_RESOURCE_SMOKE_CONFIG:-$ROOT/deploy/docker/kiln.docker.toml.example}
KEYS=${KILN_FULL_RESOURCE_SMOKE_KEYS:-$ROOT/configs/examples/kiln.keys}
CONTAINER="kiln-full-resource-$$"

command -v docker >/dev/null 2>&1 || {
  echo 'docker is required' >&2
  exit 2
}
test -f "$CONFIG" || {
  echo "full resource smoke config not found: $CONFIG" >&2
  exit 2
}
test -f "$KEYS" || {
  echo "full resource smoke keys not found: $KEYS" >&2
  exit 2
}

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

docker run -d --name "$CONTAINER" \
  --cpus=2 --memory=384m --memory-swap=384m \
  -v "$CONFIG:/etc/kiln/kiln.toml:ro" \
  -v "$KEYS:/etc/kiln/kiln.keys:ro" \
  "$IMAGE" >/dev/null

attempt=1
while [ "$attempt" -le 15 ]; do
  if docker exec "$CONTAINER" wget -q -O /dev/null \
    http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
    break
  fi
  if [ "$attempt" -eq 15 ]; then
    docker logs "$CONTAINER" >&2
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 1
done

logs=$(docker logs "$CONTAINER" 2>&1)
for value in \
  'runtime_variant=full' \
  'resource_profile=balanced' \
  'effective_memory_mb=384' \
  'memory_limit_mb=96' \
  'packager_engine=auto' \
  'ffmpeg_available=true' \
  'FFmpeg memory is outside the Go soft limit' \
  'ffmpeg_scope=subprocess' \
  'advisory_only=true'
do
  if ! printf '%s\n' "$logs" | grep -Fq "$value"; then
    echo "$CONTAINER: missing log value: $value" >&2
    printf '%s\n' "$logs" >&2
    exit 1
  fi
done

echo 'RESULT: PASS - full image reports FFmpeg memory as advisory-only.'
