#!/bin/sh
set -eu

IMAGE=${1:-kiln:lite-local}
BUSYBOX_IMAGE=${2:-busybox:1.37.0}
CHECK_RESOURCES=${KILN_LITE_CHECK_RESOURCES:-1}
CPUS=${KILN_LITE_CPUS:-1}
MEMORY=${KILN_LITE_MEMORY:-64m}
MAX_PEAK_BYTES=${KILN_LITE_MAX_PEAK_BYTES:-62914560}
ITERATIONS=${KILN_LITE_SMOKE_ITERATIONS:-10}
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
RUN_ID=${KILN_LITE_RUN_ID:-$$}
NETWORK=kiln-lite-smoke-$RUN_ID
ORIGIN=kiln-lite-origin-$RUN_ID
SERVER=kiln-lite-smoke-$RUN_ID
PERF_SERVER=kiln-lite-performance-$RUN_ID
DATA_VOLUME=kiln-lite-data-$RUN_ID
PERF_DATA_VOLUME=kiln-lite-performance-data-$RUN_ID
PROBE_VOLUME=kiln-lite-probe-$RUN_ID

cleanup() {
  docker rm -f "$ORIGIN" "$SERVER" "$PERF_SERVER" >/dev/null 2>&1 || true
  docker volume rm -f "$DATA_VOLUME" >/dev/null 2>&1 || true
  docker volume rm -f "$PERF_DATA_VOLUME" >/dev/null 2>&1 || true
  docker volume rm -f "$PROBE_VOLUME" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

docker network create "$NETWORK" >/dev/null
docker volume create "$DATA_VOLUME" >/dev/null
docker volume create "$PROBE_VOLUME" >/dev/null
docker run --rm --mount "type=volume,src=$PROBE_VOLUME,dst=/probe" "$BUSYBOX_IMAGE" \
  sh -c 'mkdir -p /probe/lib && cp /bin/busybox /probe/busybox &&
    cp /lib/ld-linux-*.so.* /probe/loader &&
    cp /lib/libc.so.6 /lib/libm.so.6 /lib/libresolv.so.2 /probe/lib/'
docker run -d --name "$ORIGIN" --network "$NETWORK" --network-alias kiln-origin \
  -v "$ROOT/testdata/cenc:/www:ro" "$BUSYBOX_IMAGE" httpd -f -p 8000 -h /www >/dev/null
docker run -d --name "$SERVER" --network "$NETWORK" \
  --cpus="$CPUS" --memory="$MEMORY" --memory-swap="$MEMORY" --read-only \
  --mount "type=volume,src=$DATA_VOLUME,dst=/var/lib/kiln" \
  --mount "type=volume,src=$PROBE_VOLUME,dst=/probe,readonly" \
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
  printf '%s\n' "$logs" | grep -q 'memory_limit_mb=24'
  printf '%s\n' "$logs" | grep -q 'inflight_mb=24'
  printf '%s\n' "$logs" | grep -q 'max_segment_mb=20'
  printf '%s\n' "$logs" | grep -q 'gc_percent=50'
  printf '%s\n' "$logs" | grep -q 'drop_file_cache=true'
fi
iteration=1
while [ "$iteration" -le "$ITERATIONS" ]; do
  docker run --rm --network "$NETWORK" -v "$ROOT:/src:ro" "$BUSYBOX_IMAGE" \
    sh /src/deploy/docker/lite-media-smoke.sh "http://$SERVER:8080"
  iteration=$((iteration + 1))
done

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

  current_bytes=$(docker exec "$SERVER" /probe/loader --library-path /probe/lib \
    /probe/busybox cat /sys/fs/cgroup/memory.current)
  peak_bytes=$(docker exec "$SERVER" /probe/loader --library-path /probe/lib \
    /probe/busybox cat /sys/fs/cgroup/memory.peak)
  anon_bytes=$(docker exec "$SERVER" /probe/loader --library-path /probe/lib \
    /probe/busybox awk '$1 == "anon" { print $2 }' /sys/fs/cgroup/memory.stat)
  file_bytes=$(docker exec "$SERVER" /probe/loader --library-path /probe/lib \
    /probe/busybox awk '$1 == "file" { print $2 }' /sys/fs/cgroup/memory.stat)
  oom_events=$(docker exec "$SERVER" /probe/loader --library-path /probe/lib \
    /probe/busybox awk '$1 == "oom" { print $2 }' /sys/fs/cgroup/memory.events)
  oom_kills=$(docker exec "$SERVER" /probe/loader --library-path /probe/lib \
    /probe/busybox awk '$1 == "oom_kill" { print $2 }' /sys/fs/cgroup/memory.events)
  if [ -z "$current_bytes" ] || [ -z "$peak_bytes" ] ||
    [ -z "$anon_bytes" ] || [ -z "$file_bytes" ]; then
    echo "FATAL: cgroup memory metrics are unavailable" >&2
    exit 1
  fi
  if [ "$peak_bytes" -gt "$MAX_PEAK_BYTES" ]; then
    echo "FATAL: lite cgroup peak is $peak_bytes bytes, want <= $MAX_PEAK_BYTES" >&2
    exit 1
  fi
  if [ "${oom_events:-0}" -ne 0 ] || [ "${oom_kills:-0}" -ne 0 ]; then
    echo "FATAL: lite cgroup reported oom=$oom_events oom_kill=$oom_kills" >&2
    exit 1
  fi
  echo "lite_cgroup_current=$current_bytes bytes"
  echo "lite_cgroup_peak=$peak_bytes bytes"
  echo "lite_cgroup_anon=$anon_bytes bytes file=$file_bytes bytes"
  echo "lite_cgroup_oom=${oom_events:-0} oom_kill=${oom_kills:-0}"
else
  echo "lite_resources=skipped (non-native architecture)"
fi

docker volume create "$PERF_DATA_VOLUME" >/dev/null
docker run -d --name "$PERF_SERVER" --network "$NETWORK" --read-only \
  --mount "type=volume,src=$PERF_DATA_VOLUME,dst=/var/lib/kiln" \
  --cap-drop=ALL --security-opt=no-new-privileges \
  -e KILN_PLAY_OPEN=1 -e KILN_RESOURCE_MODE=performance \
  -v "$ROOT/deploy/docker/core-smoke.toml:/etc/kiln/kiln.toml:ro" \
  -v "$ROOT/deploy/docker/core-smoke.keys:/etc/kiln/kiln.keys:ro" \
  "$IMAGE" >/dev/null
attempt=1
while [ "$attempt" -le 15 ]; do
  if docker exec "$PERF_SERVER" /usr/local/bin/kiln \
    -healthcheck http://127.0.0.1:8080/readyz; then
    break
  fi
  if [ "$attempt" -eq 15 ]; then
    docker logs "$PERF_SERVER"
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 1
done
performance_logs=$(docker logs "$PERF_SERVER" 2>&1)
printf '%s\n' "$performance_logs" | grep -q 'resource_mode=performance'
printf '%s\n' "$performance_logs" | grep -q 'resource_constrained=false'
printf '%s\n' "$performance_logs" | grep -q 'drop_file_cache=false'
echo "RESULT: PASS - lite fixture smoke and cgroup instrumentation hold."
