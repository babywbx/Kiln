#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
CONFIG=${KILN_PERF_CONFIG:-$ROOT/configs/local.toml}
CHANNELS=${KILN_PERF_CHANNELS:-demo-news,demo-uhd}
CAPTURE_SECONDS=${KILN_PERF_CAPTURE_SECONDS:-8}
LISTEN=${KILN_PERF_LISTEN:-127.0.0.1:18080}
OUTPUT=${KILN_PERF_OUTPUT:-}
KEEP=${KILN_PERF_KEEP:-0}
CURL=${KILN_PERF_CURL:-curl}
FFMPEG=${KILN_PERF_FFMPEG:-ffmpeg}
FFPROBE=${KILN_PERF_FFPROBE:-ffprobe}
DATE=${KILN_PERF_DATE:-date}
PERL=${KILN_PERF_PERL:-perl}

usage() {
  cat <<'EOF'
usage: scripts/live-performance.sh [--validate|--list-channels]
       scripts/live-performance.sh --select-highest-variant FILE MASTER_URL
       scripts/live-performance.sh --score-capture BYTES ELAPSED_MS DURATION_S

Runs an explicit local end-to-end performance test with configs/local.toml.
It starts an isolated Kiln data directory, measures cold startup and first
manifest latency, captures each configured live channel, and measures offline
software decode speed. This command is intentionally not part of normal CI.

Environment:
  KILN_PERF_CONFIG           config path (default: configs/local.toml)
  KILN_PERF_CHANNELS         comma-separated channel IDs
  KILN_PERF_CAPTURE_SECONDS  positive integer seconds per channel (default: 8)
  KILN_PERF_LISTEN           isolated listen address (default: 127.0.0.1:18080)
  KILN_PERF_BINARY           existing Kiln binary; otherwise builds a release
  KILN_PERF_OUTPUT           optional report output path
  KILN_PERF_KEEP=1           keep temporary captures and logs
EOF
}

validate_inputs() {
  case "$CAPTURE_SECONDS" in
    ''|*[!0-9]*|0)
      echo 'KILN_PERF_CAPTURE_SECONDS must be a positive integer' >&2
      return 2
      ;;
  esac
  case ",$CHANNELS," in
    *,,*)
      echo 'KILN_PERF_CHANNELS must not contain empty channel IDs' >&2
      return 2
      ;;
  esac
  old_ifs=$IFS
  IFS=,
  for channel in $CHANNELS; do
    case "$channel" in
      ''|*[!A-Za-z0-9._-]*)
        echo "invalid channel ID: $channel" >&2
        IFS=$old_ifs
        return 2
        ;;
    esac
  done
  IFS=$old_ifs
}

select_highest_variant() {
  manifest=$1
  master_url=$2
  variant=$(awk '
    /^#EXT-X-STREAM-INF:/ {
      info = $0
      if (getline uri <= 0) next
      sub(/\r$/, "", uri)
      score = 0
      if (match(info, /RESOLUTION=[0-9]+x[0-9]+/)) {
        resolution = substr(info, RSTART + 11, RLENGTH - 11)
        split(resolution, dimensions, "x")
        score = dimensions[1] * dimensions[2]
      }
      if (best_uri == "" || score > best_score) {
        best_uri = uri
        best_score = score
      }
    }
    END { if (best_uri != "") print best_uri }
  ' "$manifest")
  if [ -z "$variant" ] && awk '/^#EXTINF:|^#EXT-X-PART:/ { found = 1 } END { exit !found }' "$manifest"; then
    printf '%s\n' "$master_url"
    return 0
  fi
  test -n "$variant" || {
    echo "no video variant found in manifest: $manifest" >&2
    return 1
  }
  case "$variant" in
    http://*|https://*)
      printf '%s\n' "$variant"
      ;;
    /*)
      origin=$(printf '%s\n' "$master_url" | sed -E 's#^(https?://[^/]+).*$#\1#')
      printf '%s%s\n' "$origin" "$variant"
      ;;
    *)
      printf '%s/%s\n' "${master_url%/*}" "$variant"
      ;;
  esac
}

score_capture() {
  bytes=$1
  elapsed_ms=$2
  duration=$3
  case "$bytes:$elapsed_ms" in
    *[!0-9:]*|0:*|*:0)
      echo 'capture bytes and elapsed milliseconds must be positive integers' >&2
      return 2
      ;;
  esac
  awk -v bytes="$bytes" -v elapsed_ms="$elapsed_ms" -v duration="$duration" '
    BEGIN {
      if (duration + 0 <= 0) exit 2
      printf "capture_bytes=%d capture_mbps=%.2f capture_realtime_ratio=%.2f capture_overrun_ms=%.0f\n",
        bytes, bytes * 8 / elapsed_ms / 1000, duration * 1000 / elapsed_ms,
        elapsed_ms - duration * 1000
    }
  '
}

case "${1:-}" in
  --help|-h)
    usage
    exit 0
    ;;
  --validate)
    validate_inputs
    exit $?
    ;;
  --list-channels)
    validate_inputs
    printf '%s\n' "$CHANNELS" | tr ',' '\n'
    exit 0
    ;;
  --select-highest-variant)
    test "$#" -eq 3 || {
      usage >&2
      exit 2
    }
    select_highest_variant "$2" "$3"
    exit $?
    ;;
  --score-capture)
    test "$#" -eq 4 || {
      usage >&2
      exit 2
    }
    score_capture "$2" "$3" "$4"
    exit $?
    ;;
  '')
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

validate_inputs
test -f "$CONFIG" || {
  echo "live performance config not found: $CONFIG" >&2
  exit 2
}
for command in "$CURL" "$FFMPEG" "$FFPROBE"; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "required command not found: $command" >&2
    exit 2
  }
done
if [ -z "${KILN_PERF_BINARY:-}" ]; then
  command -v go >/dev/null 2>&1 || {
    echo 'go is required when KILN_PERF_BINARY is not set' >&2
    exit 2
  }
fi
if "$CURL" -fsS --max-time 1 "http://$LISTEN/readyz" >/dev/null 2>&1; then
  echo "listen address is already serving traffic: $LISTEN" >&2
  exit 2
fi

WORK=$(mktemp -d "${TMPDIR:-/tmp}/kiln-live-performance.XXXXXX")
SERVER_PID=
SAMPLER_PID=

cleanup() {
  if [ -n "$SAMPLER_PID" ]; then
    kill "$SAMPLER_PID" >/dev/null 2>&1 || true
    wait "$SAMPLER_PID" 2>/dev/null || true
  fi
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    sleep 1
    kill -9 "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ "$KEEP" = 1 ]; then
    echo "live performance artifacts kept at: $WORK" >&2
  else
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT HUP INT TERM

emit() {
  if [ -n "$OUTPUT" ]; then
    printf '%s\n' "$*" >>"$OUTPUT"
  fi
  printf '%s\n' "$*"
}

now_ns() {
  value=$("$DATE" +%s%N 2>/dev/null || true)
  case "$value" in
    ''|*[!0-9]*) ;;
    *)
      if [ "${#value}" -ge 16 ]; then
        echo "$value"
        return 0
      fi
      ;;
  esac
  command -v "$PERL" >/dev/null 2>&1 || {
    echo 'a nanosecond-capable date or Perl Time::HiRes is required' >&2
    return 2
  }
  "$PERL" -MTime::HiRes=time -e 'printf "%.0f\n", time() * 1000000000'
}

milliseconds_between() {
  echo $(( ($2 - $1) / 1000000 ))
}

if [ -n "$OUTPUT" ]; then
  : >"$OUTPUT"
fi

BINARY=${KILN_PERF_BINARY:-$WORK/kiln}
if [ -z "${KILN_PERF_BINARY:-}" ]; then
  go build -trimpath -ldflags='-s -w' -o "$BINARY" ./apps/server
fi

mkdir -p "$WORK/data"
server_started=$(now_ns)
KILN_DATA_DIR="$WORK/data" \
KILN_LISTEN="$LISTEN" \
KILN_PLAY_OPEN=1 \
  "$BINARY" -config "$CONFIG" >"$WORK/kiln.log" 2>&1 &
SERVER_PID=$!

ready=0
attempt=1
while [ "$attempt" -le 300 ]; do
  if "$CURL" -fsS --max-time 1 "http://$LISTEN/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo 'Kiln exited before becoming ready' >&2
    tail -40 "$WORK/kiln.log" >&2
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 0.1
done
if [ "$ready" != 1 ]; then
  echo 'Kiln did not become ready within 30 seconds' >&2
  tail -40 "$WORK/kiln.log" >&2
  exit 1
fi
server_ready=$(now_ns)

sample_process() {
  while kill -0 "$SERVER_PID" >/dev/null 2>&1; do
    ps -o rss= -o %cpu= -p "$SERVER_PID" 2>/dev/null || true
    sleep 1
  done
}
sample_process >"$WORK/process.samples" &
SAMPLER_PID=$!

emit "server_start_ms=$(milliseconds_between "$server_started" "$server_ready")"
emit "capture_seconds=$CAPTURE_SECONDS"

old_ifs=$IFS
IFS=,
for channel in $CHANNELS; do
  IFS=$old_ifs
  play_url="http://$LISTEN/v1/play/$channel/index.m3u8"
  manifest="$WORK/$channel.m3u8"
  manifest_metrics=$("$CURL" -fsS --max-time 90 \
    -o "$manifest" \
    -w '%{http_code} %{time_starttransfer} %{time_total} %{size_download}' \
    "$play_url") || {
      echo "$channel: first manifest request failed" >&2
      tail -60 "$WORK/kiln.log" >&2
      exit 1
    }
  IFS=' ' read -r manifest_http manifest_ttfb manifest_total manifest_bytes <<EOF
$manifest_metrics
EOF
  manifest_ttfb_ms=$(awk -v seconds="$manifest_ttfb" 'BEGIN { printf "%.0f", seconds * 1000 }')
  manifest_total_ms=$(awk -v seconds="$manifest_total" 'BEGIN { printf "%.0f", seconds * 1000 }')
  capture_url=$(select_highest_variant "$manifest" "$play_url")

  capture="$WORK/$channel.mkv"
  capture_started=$(now_ns)
  "$FFMPEG" -nostdin -hide_banner -loglevel warning -y \
    -rw_timeout 30000000 -analyzeduration 5000000 -probesize 20000000 \
    -i "$capture_url" -t "$CAPTURE_SECONDS" \
    -map 0:v:0 -c copy "$capture" \
    >"$WORK/$channel-capture.log" 2>&1 || {
      echo "$channel: live capture failed" >&2
      tail -60 "$WORK/$channel-capture.log" >&2
      exit 1
    }
  capture_ended=$(now_ns)
  capture_ms=$(milliseconds_between "$capture_started" "$capture_ended")

  probe=$("$FFPROBE" -v error -select_streams v:0 \
    -show_entries stream=codec_name,profile,width,height,r_frame_rate \
    -of csv=p=0 "$capture")
  media_duration=$("$FFPROBE" -v error -show_entries format=duration \
    -of default=noprint_wrappers=1:nokey=1 "$capture")
  capture_bytes=$(wc -c <"$capture" | tr -d ' ')
  capture_score=$(score_capture "$capture_bytes" "$capture_ms" "$media_duration")

  decode_started=$(now_ns)
  "$FFMPEG" -nostdin -hide_banner -loglevel error -i "$capture" \
    -map 0:v:0 -an -f null - >"$WORK/$channel-decode.log" 2>&1 || {
      echo "$channel: offline decode failed" >&2
      tail -60 "$WORK/$channel-decode.log" >&2
      exit 1
    }
  decode_ended=$(now_ns)
  decode_ms=$(milliseconds_between "$decode_started" "$decode_ended")
  decode_speed=$(awk -v duration="$media_duration" -v elapsed_ms="$decode_ms" \
    'BEGIN { if (elapsed_ms <= 0) print "0.00"; else printf "%.2f", duration / (elapsed_ms / 1000) }')

  emit "channel=$channel manifest_http=$manifest_http manifest_ttfb_ms=$manifest_ttfb_ms manifest_total_ms=$manifest_total_ms manifest_bytes=$manifest_bytes capture_ms=$capture_ms $capture_score video=$probe decode_ms=$decode_ms decode_speed=${decode_speed}x"
  IFS=,
done
IFS=$old_ifs

peak_rss_mb=$(awk 'NF >= 1 && $1 > max { max = $1 } END { printf "%.1f", max / 1024 }' "$WORK/process.samples")
peak_cpu=$(awk 'NF >= 2 && $2 > max { max = $2 } END { printf "%.1f", max }' "$WORK/process.samples")
native_sessions=$(grep -c 'engine=native_rewrite' "$WORK/kiln.log" 2>/dev/null || true)
emit "server_peak_rss_mb=$peak_rss_mb server_peak_cpu_percent=$peak_cpu native_session_events=$native_sessions"
