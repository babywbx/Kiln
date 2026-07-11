#!/bin/sh
set -eu

FF="${1:-ffmpeg}"
FIXTURES="${2:-testdata/cenc}"
ORIGIN_URL="${ORIGIN_URL:-}"

command -v "$FF" >/dev/null 2>&1 || { echo "FATAL: ffmpeg not found: $FF" >&2; exit 1; }
[ -d "$FIXTURES" ] || { echo "FATAL: fixtures not found: $FIXTURES" >&2; exit 1; }

KEY=$(cat "$FIXTURES/key.txt")
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail=0
ok()  { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; fail=1; }

transcode() {
  out="$1"; input="$2"; mode="$3"; key="$4"
  mkdir -p "$out"
  set -- -hide_banner -loglevel error -y \
    -protocol_whitelist file,http,https,tcp,tls,crypto,httpproxy \
    -fflags +genpts+discardcorrupt
  [ "$mode" = remote ] && set -- "$@" -reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 5
  [ -n "$key" ] && set -- "$@" -cenc_decryption_key "$key"
  set -- "$@" -i "$input" \
    -map 0:v:0 -map "0:a:0?" \
    -c copy -tag:v hvc1 -avoid_negative_ts make_zero \
    -f hls -hls_time 2 -hls_list_size 8 \
    -hls_flags delete_segments+append_list+omit_endlist \
    -hls_segment_filename "$out/seg_%05d.ts" "$out/index.m3u8"
  "$FF" "$@" >"$out/ffmpeg.log" 2>&1
}

decode_errors() {
  "$FF" -hide_banner -v error -i "$1" -f null - 2>&1 | grep -c . || true
}

check() {
  label="$1"; codec="$2"; input="$3"; mode="$4"
  out="$WORK/$codec-$mode"

  if ! transcode "$out" "$input" "$mode" "$KEY"; then
    bad "$label: ffmpeg exited non-zero"
    sed -n '1,3p' "$out/ffmpeg.log" | sed 's/^/        /'
    return
  fi
  segs=$(find "$out" -name 'seg_*.ts' -size +0c | wc -l | tr -d ' ')
  if [ "$segs" -lt 1 ]; then bad "$label: no non-empty .ts segments"; return; fi
  [ -s "$out/index.m3u8" ] || { bad "$label: index.m3u8 missing or empty"; return; }

  errs=$(decode_errors "$out/seg_00000.ts")
  if [ "$errs" -ne 0 ]; then
    bad "$label: decrypted segment does not decode cleanly ($errs error lines)"
    return
  fi
  ok "$label: $segs segments, clean decode"
}

check_negative() {
  codec="$1"; input="$2"
  out="$WORK/$codec-nokey"
  transcode "$out" "$input" local "" || true
  if [ ! -s "$out/seg_00000.ts" ]; then
    ok "negative $codec: ffmpeg produced nothing without a key"
    return
  fi
  errs=$(decode_errors "$out/seg_00000.ts")
  if [ "$errs" -eq 0 ]; then
    bad "negative $codec: segment decoded cleanly WITHOUT the key - fixture is not encrypted, this suite proves nothing"
  else
    ok "negative $codec: garbage without the key ($errs error lines)"
  fi
}

echo "== DASH + CENC -> HLS/TS smoke =="
"$FF" -hide_banner -version | head -1
echo "-- local MPD (file protocol) --"
for codec in h264 hevc; do
  check "$codec local" "$codec" "$(cd "$FIXTURES/$codec" && pwd)/stream.mpd" local
done

echo "-- encryption is real (must fail without the key) --"
for codec in h264 hevc; do
  check_negative "$codec" "$(cd "$FIXTURES/$codec" && pwd)/stream.mpd"
done

if [ -n "$ORIGIN_URL" ]; then
  echo "-- remote MPD over HTTP ($ORIGIN_URL) --"
  for codec in h264 hevc; do
    check "$codec remote" "$codec" "$ORIGIN_URL/$codec/stream.mpd" remote
  done
else
  echo "-- remote MPD over HTTP: skipped (set ORIGIN_URL to enable) --"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "RESULT: FAILED" >&2
  exit 1
fi
echo "RESULT: PASS"
