#!/bin/sh
set -eu

BASE_URL=${1:-http://kiln-lite-smoke:8080}
CHANNEL=${2:-core-h264}
HLS_CHANNEL=${3:-core-hls}
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT INT TERM

absolute_url() {
  case "$1" in
    http://*|https://*) printf '%s\n' "$1" ;;
    /*) printf '%s%s\n' "$BASE_URL" "$1" ;;
    *) printf '%s/%s\n' "$BASE_URL" "$1" ;;
  esac
}

master=$(wget -q -O - "$BASE_URL/v1/play/$CHANNEL/index.m3u8")
printf '%s\n' "$master" | grep -q '^#EXTM3U'
media_path=$(printf '%s\n' "$master" | awk '!/^#/ && /\.m3u8/ { print; exit }')
test -n "$media_path"

media=$(wget -q -O - "$(absolute_url "$media_path")")
printf '%s\n' "$media" | grep -q '^#EXTM3U'
init_path=$(printf '%s\n' "$media" | sed -n 's/.*URI="\([^"]*\.mp4[^"]*\)".*/\1/p' | head -n 1)
segment_path=$(printf '%s\n' "$media" | awk '!/^#/ && /\.m4s/ { print; exit }')
test -n "$init_path"
test -n "$segment_path"

wget -q -O "$temporary/init.mp4" "$(absolute_url "$init_path")"
wget -q -O "$temporary/segment.m4s" "$(absolute_url "$segment_path")"
test -s "$temporary/init.mp4"
test -s "$temporary/segment.m4s"

printf 'init=%s bytes\nsegment=%s bytes\n' \
  "$(wc -c < "$temporary/init.mp4" | tr -d ' ')" \
  "$(wc -c < "$temporary/segment.m4s" | tr -d ' ')"

hls=$(wget -q -O - "$BASE_URL/v1/play/$HLS_CHANNEL/index.m3u8")
printf '%s\n' "$hls" | grep -q '^#EXTM3U'
printf '%s\n' "$hls" | grep -q '^#EXT-X-MAP:'
hls_init_path=$(printf '%s\n' "$hls" | sed -n 's/.*URI="\([^"]*\)".*/\1/p' | head -n 1)
hls_segment_path=$(printf '%s\n' "$hls" | awk '!/^#/ && NF { print; exit }')
test -n "$hls_init_path"
test -n "$hls_segment_path"

wget -q -O "$temporary/hls-init.mp4" "$(absolute_url "$hls_init_path")"
wget -q -O "$temporary/hls-segment.m4s" "$(absolute_url "$hls_segment_path")"
test -s "$temporary/hls-init.mp4"
test -s "$temporary/hls-segment.m4s"

printf 'hls_init=%s bytes\nhls_segment=%s bytes\n' \
  "$(wc -c < "$temporary/hls-init.mp4" | tr -d ' ')" \
  "$(wc -c < "$temporary/hls-segment.m4s" | tr -d ' ')"
echo "RESULT: PASS - lite native media chain holds."
