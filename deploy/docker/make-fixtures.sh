#!/usr/bin/env bash
set -euo pipefail

FF="${FFMPEG:-ffmpeg}"
OUT="${1:-testdata/cenc}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

KEY=00112233445566778899aabbccddeeff
KID=ffeeddccbbaa99887766554433221100

CENC=(
  -movflags +frag_keyframe+empty_moov+default_base_moof+skip_sidx
  -frag_duration 2000000
  -encryption_scheme cenc-aes-ctr
  -encryption_key "$KEY"
  -encryption_kid "$KID"
  -f mp4
)

build() {
  local name="$1" venc="$2" vcodec="$3"
  shift 3
  local d="$OUT/$name"
  rm -rf "$d"
  mkdir -p "$d"

  "$FF" -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=320x180:rate=25:duration=4" \
    -an -c:v "$venc" -preset ultrafast -g 50 -b:v 120k -pix_fmt yuv420p "$@" \
    "${CENC[@]}" "$TMP/$name-v.mp4"

  "$FF" -hide_banner -loglevel error -y \
    -f lavfi -i "sine=frequency=440:duration=4" \
    -vn -c:a aac -b:a 32k -ac 2 -ar 44100 \
    "${CENC[@]}" "$TMP/$name-a.mp4"

  go run scripts/make-cenc-fixture.go \
    -video "$TMP/$name-v.mp4" -audio "$TMP/$name-a.mp4" \
    -out "$d" -vcodec "$vcodec" -width 320 -height 180 -duration 4.0

  local f
  for f in "$d"/init-stream0.m4s "$d"/chunk-stream0-00001.m4s; do
    if ! strings -a "$f" | grep -qE 'tenc|senc|encv'; then
      echo "FATAL: $f carries no CENC boxes - fixture is not encrypted" >&2
      exit 1
    fi
  done
  echo "  $name: $(du -sh "$d" | cut -f1), CENC boxes present"
}

mkdir -p "$OUT"
build h264 libx264 "avc1.42c00c"
build hevc libx265 "hvc1.1.6.L60.90" -tag:v hvc1

printf '%s\n' "$KEY" > "$OUT/key.txt"
printf '%s\n' "$KID" > "$OUT/kid.txt"
echo "key=$KEY kid=$KID"
