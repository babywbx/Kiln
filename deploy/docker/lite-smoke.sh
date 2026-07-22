#!/bin/sh
set -eu

IMAGE=${1:-kiln:lite-local}
BUSYBOX_IMAGE=${2:-busybox:1.37.0}
CHECK_RESOURCES=${KILN_LITE_CHECK_RESOURCES:-1}
CPUS=${KILN_LITE_CPUS:-1}
MEMORY=${KILN_LITE_MEMORY:-64m}
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
NETWORK=kiln-lite-smoke
ORIGIN=kiln-lite-origin
SERVER=kiln-lite-smoke

cleanup() {
  docker rm -f "$ORIGIN" "$SERVER" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

docker network create "$NETWORK" >/dev/null
docker run -d --name "$ORIGIN" --network "$NETWORK" --network-alias kiln-origin \
  -v "$ROOT/testdata/cenc:/www:ro" "$BUSYBOX_IMAGE" httpd -f -p 8000 -h /www >/dev/null
docker run -d --name "$SERVER" --network "$NETWORK" \
  --cpus="$CPUS" --memory="$MEMORY" --memory-swap="$MEMORY" --read-only \
  --tmpfs /var/lib/kiln:rw,nosuid,nodev,size=48m,uid=999,gid=999 \
  --cap-drop=ALL --security-opt=no-new-privileges \
  -e KILN_PLAY_OPEN=1 \
  -v "$ROOT/deploy/docker/core-smoke.toml:/etc/kiln/kiln.toml:ro" \
  -v "$ROOT/deploy/docker/core-smoke.keys:/etc/kiln/kiln.keys:ro" \
  "$IMAGE" >/dev/null

attempt=1
while [ "$attempt" -le 15 ]; do
  if docker run --rm --network "container:$SERVER" "$BUSYBOX_IMAGE" \
    wget -q -O /dev/null http://127.0.0.1:8080/readyz; then
    break
  fi
  if [ "$attempt" -eq 15 ]; then
    docker logs "$SERVER"
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 1
done

docker exec "$SERVER" /usr/local/bin/kiln -healthcheck http://127.0.0.1:8080/healthz
logs=$(docker logs "$SERVER" 2>&1)
if [ "$CHECK_RESOURCES" = "1" ]; then
  printf '%s\n' "$logs" | grep -q 'resource_constrained=true'
  printf '%s\n' "$logs" | grep -q 'memory_limit_mb=40'
  printf '%s\n' "$logs" | grep -q 'inflight_mb=24'
fi
docker run --rm --network "$NETWORK" -v "$ROOT:/src:ro" "$BUSYBOX_IMAGE" \
  sh /src/deploy/docker/lite-media-smoke.sh

process=$(docker run --rm --pid "container:$SERVER" "$BUSYBOX_IMAGE" \
  sh -c "tr '\\000' ' ' < /proc/1/cmdline")
case "$process" in
  *"/usr/local/bin/kiln"*) ;;
  *)
    echo "FATAL: PID 1 is not kiln: $process" >&2
    exit 1
    ;;
esac
if [ "$CHECK_RESOURCES" = "1" ]; then
  rss_kb=$(docker run --rm --pid "container:$SERVER" "$BUSYBOX_IMAGE" \
    awk '/^VmRSS:/ { print $2; exit }' /proc/1/status)
  if [ -z "$rss_kb" ] || [ "$rss_kb" -gt 32768 ]; then
    echo "FATAL: lite RSS is ${rss_kb:-unknown} KiB, want <= 32768 KiB" >&2
    exit 1
  fi
  echo "lite_rss=$rss_kb KiB"
else
  echo "lite_resources=skipped (non-native architecture)"
fi
echo "RESULT: PASS - lite constrained runtime contract holds."
