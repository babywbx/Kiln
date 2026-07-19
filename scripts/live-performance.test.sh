#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT/scripts/live-performance.sh"

channels=$("$SCRIPT" --list-channels)
expected='demo-news
demo-uhd'
if [ "$channels" != "$expected" ]; then
  printf 'default live performance channels:\n%s\nwant:\n%s\n' "$channels" "$expected" >&2
  exit 1
fi

KILN_PERF_CAPTURE_SECONDS=8 "$SCRIPT" --validate

selected=$("$SCRIPT" --select-highest-variant \
  "$ROOT/testdata/liveperf/highest-master.m3u8" \
  'http://127.0.0.1:8080/v1/play/news/index.m3u8')
if [ "$selected" != 'http://127.0.0.1:8080/v1/play/news/video-high.m3u8' ]; then
  echo "highest live variant = $selected" >&2
  exit 1
fi

score=$("$SCRIPT" --score-capture 8000000 8000 8.000)
expected_score='capture_bytes=8000000 capture_mbps=8.00 capture_realtime_ratio=1.00 capture_overrun_ms=0'
if [ "$score" != "$expected_score" ]; then
  echo "capture score = $score" >&2
  exit 1
fi

if KILN_PERF_CAPTURE_SECONDS=1.5 "$SCRIPT" --validate >/dev/null 2>&1; then
  echo 'fractional capture duration was accepted' >&2
  exit 1
fi

if KILN_PERF_CHANNELS='demo-news,,demo-uhd' "$SCRIPT" --validate >/dev/null 2>&1; then
  echo 'empty channel identifier was accepted' >&2
  exit 1
fi

echo 'live performance CLI tests passed'
