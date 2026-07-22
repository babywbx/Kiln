#!/bin/sh
set -eu

BINARY=${1:-dist/kiln-lite}
MAX_BYTES=${KILN_LITE_MAX_BYTES:-10485760}

if [ ! -x "$BINARY" ]; then
  echo "lite binary is missing or not executable: $BINARY" >&2
  exit 1
fi

version_output=$("$BINARY" -version)
case "$version_output" in
  *"variant=lite"*) ;;
  *)
    echo "lite binary did not identify itself: $version_output" >&2
    exit 1
    ;;
esac

build_info=$(go version -m "$BINARY")
for forbidden in modernc.org/sqlite go.opentelemetry.io/ google.golang.org/grpc google.golang.org/protobuf; do
  if printf '%s\n' "$build_info" | grep -Fq "$forbidden"; then
    echo "lite binary contains forbidden dependency: $forbidden" >&2
    exit 1
  fi
done

size=$(wc -c < "$BINARY" | tr -d ' ')
if [ "$size" -gt "$MAX_BYTES" ]; then
  echo "lite binary is too large: $size > $MAX_BYTES bytes" >&2
  exit 1
fi

printf 'lite=%s bytes\n' "$size"
echo "RESULT: PASS - lite binary contract holds."
