#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
SCRIPT="$ROOT/deploy/docker/resource-profile-smoke.sh"
MODE=${1:-basic}

basic=$("$SCRIPT" --list basic)
expected_basic='1c1g
2c2g
4c4g'
if [ "$basic" != "$expected_basic" ]; then
  printf 'basic profiles:\n%s\nwant:\n%s\n' "$basic" "$expected_basic" >&2
  exit 1
fi

case "$MODE" in
  basic)
    ;;
  extended)
    extended=$("$SCRIPT" --list extended)
    expected_extended='1c1g
2c2g
4c4g
fractional-cgroup
performance-override'
    if [ "$extended" != "$expected_extended" ]; then
      printf 'extended profiles:\n%s\nwant:\n%s\n' "$extended" "$expected_extended" >&2
      exit 1
    fi
    ;;
  *)
    echo 'usage: resource-profile-smoke.test.sh [basic|extended]' >&2
    exit 2
    ;;
esac

if "$SCRIPT" --list unknown >/dev/null 2>&1; then
  echo 'unknown resource profile mode was accepted' >&2
  exit 1
fi

echo "$MODE resource profile smoke CLI tests passed"
