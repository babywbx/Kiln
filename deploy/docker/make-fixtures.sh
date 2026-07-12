#!/usr/bin/env bash
set -euo pipefail

FF="${FFMPEG:-ffmpeg}"
OUT="${1:-testdata/cenc}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

KEY=00112233445566778899aabbccddeeff
KID=ffeeddccbbaa99887766554433221100

# The multikid fixture carries a different key per track. It is the case the
# ffmpeg path cannot serve: its dash demuxer takes one key and would decode the
# other track to garbage.
VKEY=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
VKID=11111111111111111111111111111111
AKEY=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
AKID=22222222222222222222222222222222

CENC=(
  -movflags +frag_keyframe+empty_moov+default_base_moof+skip_sidx
  -frag_duration 2000000
  -encryption_scheme cenc-aes-ctr
  -f mp4
)

build() {
  local name="$1" venc="$2" vcodec="$3" vkey="$4" vkid="$5" akey="$6" akid="$7"
  shift 7
  local d="$OUT/$name"
  rm -rf "$d"
  mkdir -p "$d"

  "$FF" -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=320x180:rate=25:duration=4" \
    -an -c:v "$venc" -preset ultrafast -g 50 -b:v 120k -pix_fmt yuv420p "$@" \
    "${CENC[@]}" -encryption_key "$vkey" -encryption_kid "$vkid" "$TMP/$name-v.mp4"

  "$FF" -hide_banner -loglevel error -y \
    -f lavfi -i "sine=frequency=440:duration=4" \
    -vn -c:a aac -b:a 32k -ac 2 -ar 44100 \
    "${CENC[@]}" -encryption_key "$akey" -encryption_kid "$akid" "$TMP/$name-a.mp4"

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
build h264     libx264 "avc1.42c00c"     "$KEY"  "$KID"  "$KEY"  "$KID"
build hevc     libx265 "hvc1.1.6.L60.90" "$KEY"  "$KID"  "$KEY"  "$KID" -tag:v hvc1
build hev1     libx265 "hev1.1.6.L60.90" "$KEY"  "$KID"  "$KEY"  "$KID" -tag:v hev1
build multikid libx264 "avc1.42c00c"     "$VKEY" "$VKID" "$AKEY" "$AKID"

printf '%s\n' "$KEY" > "$OUT/key.txt"
printf '%s\n' "$KID" > "$OUT/kid.txt"
printf '%s:%s\n%s:%s\n' "$VKID" "$VKEY" "$AKID" "$AKEY" > "$OUT/multikid/keys.txt"
echo "key=$KEY kid=$KID"
echo "multikid: video $VKID:$VKEY audio $AKID:$AKEY"
