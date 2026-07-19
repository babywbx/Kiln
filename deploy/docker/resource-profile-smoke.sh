#!/bin/sh
set -eu

profile_list() {
  case "$1" in
    basic)
      printf '%s\n' 1c1g 2c2g 4c4g
      ;;
    extended)
      printf '%s\n' 1c1g 2c2g 4c4g fractional-cgroup performance-override
      ;;
    *)
      return 2
      ;;
  esac
}

if [ "${1:-}" = "--list" ]; then
  profile_list "${2:-}"
  exit $?
fi

MODE=${1:-basic}
IMAGE=${2:-kiln:core-local}
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
CONFIG=${KILN_RESOURCE_SMOKE_CONFIG:-$ROOT/deploy/docker/resource-smoke.toml}
RUN_ID=$$
ACTIVE_CONTAINER=

profile_list "$MODE" >/dev/null || {
  echo "usage: $0 [basic|extended] [core-image]" >&2
  exit 2
}
command -v docker >/dev/null 2>&1 || {
  echo 'docker is required' >&2
  exit 2
}
test -f "$CONFIG" || {
  echo "resource smoke config not found: $CONFIG" >&2
  exit 2
}

cleanup() {
  if [ -n "$ACTIVE_CONTAINER" ]; then
    docker rm -f "$ACTIVE_CONTAINER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT HUP INT TERM

wait_ready() {
  container=$1
  attempt=1
  while [ "$attempt" -le 15 ]; do
    if docker exec "$container" wget -q -O /dev/null http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
      return 0
    fi
    if [ "$attempt" -eq 15 ]; then
      docker logs "$container" >&2
      return 1
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
}

assert_log() {
  container=$1
  value=$2
  if ! docker logs "$container" 2>&1 | grep -Fq "$value"; then
    echo "$container: missing log value: $value" >&2
    docker logs "$container" >&2
    return 1
  fi
}

start_profile() {
  profile=$1
  shift
  ACTIVE_CONTAINER="kiln-resource-${profile}-${RUN_ID}"
  docker run -d --name "$ACTIVE_CONTAINER" "$@" \
    -v "$CONFIG:/etc/kiln/kiln.toml:ro" "$IMAGE" >/dev/null
  wait_ready "$ACTIVE_CONTAINER"
}

finish_profile() {
  echo "resource profile passed: $1"
  docker rm -f "$ACTIVE_CONTAINER" >/dev/null
  ACTIVE_CONTAINER=
}

run_1c1g() {
  start_profile 1c1g --cpus=1 --memory=1g --memory-swap=1g
  assert_log "$ACTIVE_CONTAINER" 'resource_constrained=true'
  assert_log "$ACTIVE_CONTAINER" 'effective_cpu_milli=1000'
  assert_log "$ACTIVE_CONTAINER" 'effective_memory_mb=1024'
  assert_log "$ACTIVE_CONTAINER" 'memory_limit_mb=640'
  assert_log "$ACTIVE_CONTAINER" 'effective_go_memory_limit_mb=640'
  assert_log "$ACTIVE_CONTAINER" 'inflight_mb=32'
  assert_log "$ACTIVE_CONTAINER" 'start_segments=1'
  assert_log "$ACTIVE_CONTAINER" 'prefetch_segments=1'
  assert_log "$ACTIVE_CONTAINER" 'epg_refresh_concurrency=1'
  assert_log "$ACTIVE_CONTAINER" 'epg_max_source_mb=8'
  finish_profile 1c1g
}

run_2c2g() {
  start_profile 2c2g --cpus=2 --memory=2g --memory-swap=2g
  assert_log "$ACTIVE_CONTAINER" 'resource_constrained=true'
  assert_log "$ACTIVE_CONTAINER" 'effective_cpu_milli=2000'
  assert_log "$ACTIVE_CONTAINER" 'effective_memory_mb=2048'
  assert_log "$ACTIVE_CONTAINER" 'memory_limit_mb=1280'
  assert_log "$ACTIVE_CONTAINER" 'effective_go_memory_limit_mb=1280'
  assert_log "$ACTIVE_CONTAINER" 'inflight_mb=64'
  assert_log "$ACTIVE_CONTAINER" 'start_segments=2'
  assert_log "$ACTIVE_CONTAINER" 'prefetch_segments=2'
  assert_log "$ACTIVE_CONTAINER" 'epg_refresh_concurrency=1'
  assert_log "$ACTIVE_CONTAINER" 'epg_max_source_mb=16'
  finish_profile 2c2g
}

run_4c4g() {
  start_profile 4c4g --cpus=4 --memory=4g --memory-swap=4g
  assert_log "$ACTIVE_CONTAINER" 'resource_constrained=false'
  assert_log "$ACTIVE_CONTAINER" 'effective_cpu_milli=4000'
  assert_log "$ACTIVE_CONTAINER" 'effective_memory_mb=4096'
  assert_log "$ACTIVE_CONTAINER" 'memory_limit_mb=0'
  assert_log "$ACTIVE_CONTAINER" 'effective_go_memory_limit_mb=0'
  assert_log "$ACTIVE_CONTAINER" 'inflight_mb=96'
  assert_log "$ACTIVE_CONTAINER" 'start_segments=3'
  assert_log "$ACTIVE_CONTAINER" 'prefetch_segments=3'
  assert_log "$ACTIVE_CONTAINER" 'epg_refresh_concurrency=0'
  assert_log "$ACTIVE_CONTAINER" 'epg_max_source_mb=64'
  finish_profile 4c4g
}

run_fractional_cgroup() {
  start_profile fractional-cgroup --cpus=1.5 --memory=768m --memory-swap=768m --cgroupns=host
  assert_log "$ACTIVE_CONTAINER" 'resource_constrained=true'
  assert_log "$ACTIVE_CONTAINER" 'effective_cpus=2'
  assert_log "$ACTIVE_CONTAINER" 'effective_cpu_milli=1500'
  assert_log "$ACTIVE_CONTAINER" 'effective_memory_mb=768'
  assert_log "$ACTIVE_CONTAINER" 'memory_limit_mb=480'
  assert_log "$ACTIVE_CONTAINER" 'effective_go_memory_limit_mb=480'
  assert_log "$ACTIVE_CONTAINER" 'inflight_mb=24'
  assert_log "$ACTIVE_CONTAINER" 'start_segments=2'
  assert_log "$ACTIVE_CONTAINER" 'epg_refresh_concurrency=1'
  assert_log "$ACTIVE_CONTAINER" 'epg_max_source_mb=6'
  finish_profile fractional-cgroup
}

run_performance_override() {
  start_profile performance-override --cpus=1 --memory=1g --memory-swap=1g \
    -e KILN_RESOURCE_MODE=performance -e GOMEMLIMIT=768MiB
  assert_log "$ACTIVE_CONTAINER" 'resource_constrained=false'
  assert_log "$ACTIVE_CONTAINER" 'effective_go_memory_limit_mb=768'
  assert_log "$ACTIVE_CONTAINER" 'memory_limit_mb=0'
  assert_log "$ACTIVE_CONTAINER" 'inflight_mb=96'
  assert_log "$ACTIVE_CONTAINER" 'start_segments=3'
  assert_log "$ACTIVE_CONTAINER" 'prefetch_segments=3'
  assert_log "$ACTIVE_CONTAINER" 'epg_refresh_concurrency=0'
  assert_log "$ACTIVE_CONTAINER" 'epg_max_source_mb=64'
  finish_profile performance-override
}

run_1c1g
run_2c2g
run_4c4g
if [ "$MODE" = extended ]; then
  run_fractional_cgroup
  run_performance_override
fi
