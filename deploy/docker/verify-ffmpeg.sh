#!/bin/sh
set -eu

FF="${1:-ffmpeg}"
command -v "$FF" >/dev/null 2>&1 || { echo "FATAL: ffmpeg not found: $FF" >&2; exit 1; }

fail=0
ok()  { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; fail=1; }

need() {
  if "$FF" -hide_banner $2 2>/dev/null | grep -Eq -e "$3"; then ok "$1"; else bad "$1"; fi
}

echo "== ffmpeg capability assertions =="
"$FF" -hide_banner -version | head -1

echo "-- build configuration --"
need "libxml2 (required for the DASH demuxer)" "-buildconf" '--enable-libxml2'
need "TLS backend (openssl/gnutls/mbedtls)"    "-buildconf" '--enable-(openssl|gnutls|mbedtls)'

echo "-- demuxers --"
need "dash demuxer (reads the MPD)"            "-demuxers" '^ *D +dash '
need "mov/mp4 demuxer (DASH segments + CENC)"  "-demuxers" '^ *D +mov,mp4'
need "hls demuxer"                             "-demuxers" '^ *D +hls '
need "mpegts demuxer"                          "-demuxers" '^ *D +mpegts '

echo "-- muxers --"
need "hls muxer (-f hls)"                      "-muxers" '^ *E +hls '
need "mpegts muxer (.ts segments)"             "-muxers" '^ *E +mpegts '
need "mp4 muxer (future fmp4 segments)"        "-muxers" '^ *E +mp4 '
need "mov muxer (-tag:v hvc1)"                 "-muxers" '^ *E +mov '

echo "-- protocols (must cover -protocol_whitelist) --"
for p in file http https tcp tls crypto httpproxy; do
  need "protocol $p" "-protocols" "^ *${p}\$"
done

echo "-- bitstream filters (MP4 AVCC/HVCC -> Annex-B for TS) --"
need "h264_mp4toannexb (H.264 -c copy into TS)" "-bsfs" '^h264_mp4toannexb$'
need "hevc_mp4toannexb (HEVC -c copy into TS)"  "-bsfs" '^hevc_mp4toannexb$'
need "aac_adtstoasc"                            "-bsfs" '^aac_adtstoasc$'

echo "-- decoders (stream_info probing, -map selection) --"
need "h264 decoder" "-decoders" '^ *V[.A-Z]* +h264 '
need "hevc decoder" "-decoders" '^ *V[.A-Z]* +hevc '
need "aac decoder"  "-decoders" '^ *A[.A-Z]* +aac '

echo "-- CENC decryption --"
need "dash: -cenc_decryption_key" "-h demuxer=dash" 'cenc_decryption_key'
need "mov: -decryption_key"       "-h demuxer=mov"  'decryption_key'

echo
if [ "$fail" -ne 0 ]; then
  echo "RESULT: FAILED - this ffmpeg cannot serve Kiln's DASH->HLS path." >&2
  exit 1
fi
echo "RESULT: PASS - all required capabilities present."
