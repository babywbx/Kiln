#!/bin/sh
set -eu

IMAGE=${1:-kiln:lite-local}
MAX_BINARY_BYTES=${KILN_LITE_MAX_BYTES:-10485760}
MAX_IMAGE_BYTES=${KILN_LITE_MAX_IMAGE_BYTES:-12582912}

temporary=$(mktemp -d)
container=
cleanup() {
  if [ -n "$container" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  rm -rf "$temporary"
}
trap cleanup EXIT INT TERM

label() {
  docker image inspect "$IMAGE" --format "{{ index .Config.Labels \"$1\" }}"
}

expect_label() {
  got=$(label "$1")
  if [ "$got" != "$2" ]; then
    echo "FATAL: $IMAGE label $1=$got, want $2" >&2
    exit 1
  fi
}

expect_nonempty_label() {
  got=$(label "$1")
  case "$got" in
    ''|'<no value>'|unknown)
      echo "FATAL: $IMAGE label $1 is not populated" >&2
      exit 1
      ;;
  esac
}

expect_label org.opencontainers.image.title "Kiln Lite"
expect_label org.opencontainers.image.description \
  "Minimal high-performance native media server."
expect_label org.opencontainers.image.url https://github.com/babywbx/Kiln
expect_label org.opencontainers.image.documentation https://github.com/babywbx/Kiln#docker
expect_label org.opencontainers.image.source https://github.com/babywbx/Kiln
expect_label org.opencontainers.image.authors Babywbx
expect_label org.opencontainers.image.vendor Babywbx
expect_label org.opencontainers.image.licenses AGPL-3.0-only
expect_label org.opencontainers.image.base.name scratch
expect_nonempty_label org.opencontainers.image.version
expect_nonempty_label org.opencontainers.image.revision
expect_nonempty_label org.opencontainers.image.created
expect_label io.kiln.variant lite
expect_label io.kiln.media.engines native
expect_label io.kiln.features playback
expect_label io.kiln.ffmpeg.available false
expect_label io.kiln.database.available false
expect_label io.kiln.admin.available false
expect_label io.kiln.epg.available false
expect_label io.kiln.telemetry.available false
expect_label io.kiln.packager.default native

user=$(docker image inspect "$IMAGE" --format '{{.Config.User}}')
if [ "$user" != "999:999" ]; then
  echo "FATAL: $IMAGE runs as $user, want 999:999" >&2
  exit 1
fi

healthcheck=$(docker image inspect "$IMAGE" --format '{{json .Config.Healthcheck.Test}}')
expected_healthcheck='["CMD","/usr/local/bin/kiln","-healthcheck","http://127.0.0.1:8080/healthz"]'
if [ "$healthcheck" != "$expected_healthcheck" ]; then
  echo "FATAL: unexpected lite healthcheck: $healthcheck" >&2
  exit 1
fi

container=$(docker create --entrypoint /usr/local/bin/kiln "$IMAGE" -version)
docker cp "$container:/usr/local/bin/kiln" "$temporary/kiln"
docker export "$container" | tar -tf - > "$temporary/rootfs"
docker rm "$container" >/dev/null
container=

binary_size=$(wc -c < "$temporary/kiln" | tr -d ' ')
if [ "$binary_size" -gt "$MAX_BINARY_BYTES" ]; then
  echo "FATAL: lite binary is too large: $binary_size > $MAX_BINARY_BYTES bytes" >&2
  exit 1
fi
build_info=$(go version -m "$temporary/kiln")
for forbidden in modernc.org/sqlite go.opentelemetry.io/ google.golang.org/grpc google.golang.org/protobuf; do
  if printf '%s\n' "$build_info" | grep -Fq "$forbidden"; then
    echo "FATAL: lite binary contains forbidden dependency: $forbidden" >&2
    exit 1
  fi
done

for required in etc/passwd etc/group etc/ssl/certs/ca-certificates.crt usr/local/bin/kiln; do
  if ! grep -qx "$required" "$temporary/rootfs"; then
    echo "FATAL: lite rootfs is missing $required" >&2
    exit 1
  fi
done
if grep -Eq '(^|/)(bin/sh|bin/busybox|usr/local/bin/ffmpeg)$' "$temporary/rootfs"; then
  echo "FATAL: lite rootfs contains a shell, BusyBox, or FFmpeg" >&2
  exit 1
fi

image_size=$(docker image inspect "$IMAGE" --format '{{.Size}}')
if [ "$image_size" -gt "$MAX_IMAGE_BYTES" ]; then
  echo "FATAL: lite image is too large: $image_size > $MAX_IMAGE_BYTES bytes" >&2
  exit 1
fi

version_output=$(docker run --rm "$IMAGE" -version)
case "$version_output" in
  *"variant=lite"*) ;;
  *)
    echo "FATAL: lite image did not identify itself: $version_output" >&2
    exit 1
    ;;
esac

printf 'lite_binary=%s bytes\nlite_image=%s bytes\n' "$binary_size" "$image_size"
echo "RESULT: PASS - lite image contract holds."
