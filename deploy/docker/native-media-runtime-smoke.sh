#!/bin/sh
set -eu

IMAGE=${1:-kiln:lite-local}
BUSYBOX_IMAGE=${2:-busybox:1.38.0@sha256:fd8d9aa63ba2f0982b5304e1ee8d3b90a210bc1ffb5314d980eb6962f1a9715d}
CHECK_RESOURCES=${KILN_SMOKE_CHECK_RESOURCES:-1}
CPUS=${KILN_SMOKE_CPUS:-1}
MEMORY=${KILN_SMOKE_MEMORY:-64m}
MAX_PEAK_BYTES=${KILN_SMOKE_MAX_PEAK_BYTES:-62914560}
MAX_RSS_KB=${KILN_SMOKE_MAX_RSS_KB:-32768}
ITERATIONS=${KILN_SMOKE_ITERATIONS:-10}
SMOKE_VARIANT=${KILN_SMOKE_VARIANT:-lite}
HEALTHCHECK_MODE=${KILN_SMOKE_HEALTHCHECK_MODE:-binary}
EXPECTED_PROFILE=${KILN_SMOKE_EXPECTED_PROFILE:-lite}
EXPECTED_MEMORY_LIMIT_MB=${KILN_SMOKE_EXPECTED_MEMORY_LIMIT_MB:-24}
EXPECTED_INFLIGHT_MB=${KILN_SMOKE_EXPECTED_INFLIGHT_MB:-24}
EXPECTED_MAX_SEGMENT_MB=${KILN_SMOKE_EXPECTED_MAX_SEGMENT_MB:-20}
EXPECTED_GC_PERCENT=${KILN_SMOKE_EXPECTED_GC_PERCENT:-50}
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
RUN_ID=${KILN_SMOKE_RUN_ID:-$$}
NETWORK=kiln-$SMOKE_VARIANT-smoke-$RUN_ID
ORIGIN=kiln-$SMOKE_VARIANT-origin-$RUN_ID
SERVER=kiln-$SMOKE_VARIANT-smoke-$RUN_ID
PERF_SERVER=kiln-$SMOKE_VARIANT-performance-$RUN_ID
DATA_VOLUME=kiln-$SMOKE_VARIANT-data-$RUN_ID
PERF_DATA_VOLUME=kiln-$SMOKE_VARIANT-performance-data-$RUN_ID
PROBE_VOLUME=kiln-$SMOKE_VARIANT-probe-$RUN_ID

cleanup() {
  docker rm -f "$ORIGIN" "$SERVER" "$PERF_SERVER" >/dev/null 2>&1 || true
  docker volume rm -f "$DATA_VOLUME" >/dev/null 2>&1 || true
  docker volume rm -f "$PERF_DATA_VOLUME" >/dev/null 2>&1 || true
  docker volume rm -f "$PROBE_VOLUME" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

check_health() {
  container=$1
  endpoint=$2
  case "$HEALTHCHECK_MODE" in
    binary)
      docker exec "$container" /usr/local/bin/kiln \
        -healthcheck "http://127.0.0.1:8080/$endpoint"
      ;;
    external)
      docker run --rm --network "container:$container" "$BUSYBOX_IMAGE" \
        wget -q -O /dev/null "http://127.0.0.1:8080/$endpoint"
      ;;
    *)
      echo "unknown healthcheck mode: $HEALTHCHECK_MODE" >&2
      return 2
      ;;
  esac
}

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

check_health "$SERVER" healthz
logs=$(docker logs "$SERVER" 2>&1)
if [ "$CHECK_RESOURCES" = "1" ]; then
  printf '%s\n' "$logs" | grep -q 'resource_constrained=true'
  printf '%s\n' "$logs" | grep -q "resource_profile=$EXPECTED_PROFILE"
  printf '%s\n' "$logs" | grep -q "memory_limit_mb=$EXPECTED_MEMORY_LIMIT_MB"
  printf '%s\n' "$logs" | grep -q "inflight_mb=$EXPECTED_INFLIGHT_MB"
  printf '%s\n' "$logs" | grep -q "max_segment_mb=$EXPECTED_MAX_SEGMENT_MB"
  printf '%s\n' "$logs" | grep -q "gc_percent=$EXPECTED_GC_PERCENT"
  printf '%s\n' "$logs" | grep -q 'drop_file_cache=true'
fi
iteration=1
while [ "$iteration" -le "$ITERATIONS" ]; do
  docker run --rm --network "$NETWORK" -v "$ROOT:/src:ro" "$BUSYBOX_IMAGE" \
    sh /src/deploy/docker/native-media-chain-smoke.sh "http://$SERVER:8080"
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
  if [ -z "$rss_kb" ] || [ "$rss_kb" -gt "$MAX_RSS_KB" ]; then
    echo "FATAL: $SMOKE_VARIANT RSS is ${rss_kb:-unknown} KiB, want <= $MAX_RSS_KB KiB" >&2
    exit 1
  fi
  echo "${SMOKE_VARIANT}_rss=$rss_kb KiB"

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
    echo "FATAL: $SMOKE_VARIANT cgroup peak is $peak_bytes bytes, want <= $MAX_PEAK_BYTES" >&2
    exit 1
  fi
  if [ "${oom_events:-0}" -ne 0 ] || [ "${oom_kills:-0}" -ne 0 ]; then
    echo "FATAL: $SMOKE_VARIANT cgroup reported oom=$oom_events oom_kill=$oom_kills" >&2
    exit 1
  fi
  echo "${SMOKE_VARIANT}_cgroup_current=$current_bytes bytes"
  echo "${SMOKE_VARIANT}_cgroup_peak=$peak_bytes bytes"
  echo "${SMOKE_VARIANT}_cgroup_anon=$anon_bytes bytes file=$file_bytes bytes"
  echo "${SMOKE_VARIANT}_cgroup_oom=${oom_events:-0} oom_kill=${oom_kills:-0}"
else
  echo "${SMOKE_VARIANT}_resources=skipped"
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
  if check_health "$PERF_SERVER" readyz; then
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
echo "RESULT: PASS - $SMOKE_VARIANT fixture smoke and cgroup instrumentation hold."
