#!/bin/sh
set -eu

CORE_IMAGE="${1:-kiln:core}"
FULL_IMAGE="${2:-kiln:full}"

label() {
  docker image inspect "$1" --format "{{ index .Config.Labels \"$2\" }}"
}

expect_label() {
  image="$1"
  key="$2"
  want="$3"
  got="$(label "$image" "$key")"
  if [ "$got" != "$want" ]; then
    echo "FATAL: $image label $key=$got, want $want" >&2
    exit 1
  fi
}

expect_label "$CORE_IMAGE" io.kiln.variant core
expect_label "$CORE_IMAGE" io.kiln.ffmpeg.available false
expect_label "$CORE_IMAGE" io.kiln.packager.default native
expect_label "$FULL_IMAGE" io.kiln.variant full
expect_label "$FULL_IMAGE" io.kiln.ffmpeg.available true
expect_label "$FULL_IMAGE" io.kiln.packager.default auto

core_base="$(label "$CORE_IMAGE" io.kiln.base.source)"
full_base="$(label "$FULL_IMAGE" io.kiln.base.source)"
case "$core_base" in
  alpine:3.24.1@sha256:*) ;;
  *)
    echo "FATAL: unexpected core base image $core_base" >&2
    exit 1
    ;;
esac
if [ "$core_base" != "$full_base" ]; then
  echo "FATAL: core and full use different base images" >&2
  exit 1
fi

for image in "$CORE_IMAGE" "$FULL_IMAGE"; do
  user="$(docker image inspect "$image" --format '{{.Config.User}}')"
  if [ "$user" != "kiln" ]; then
    echo "FATAL: $image runs as $user, want kiln" >&2
    exit 1
  fi
done

docker run --rm --entrypoint /bin/sh "$CORE_IMAGE" -ec '
  test "$KILN_DEFAULT_PACKAGER_ENGINE" = native
  test ! -e /usr/local/bin/ffmpeg
  test "$(id -u)" = 999
  grep -qx "variant=core" /usr/local/share/kiln/build-verified
  grep -qx "ffmpeg=absent" /usr/local/share/kiln/build-verified
  grep -qx "packager_default=native" /usr/local/share/kiln/build-verified
'

docker run --rm --entrypoint /bin/sh "$FULL_IMAGE" -ec '
  test "$KILN_DEFAULT_PACKAGER_ENGINE" = auto
  test -x /usr/local/bin/ffmpeg
  test "$(id -u)" = 999
  grep -qx "variant=full" /usr/local/share/kiln/build-verified
  grep -q "^ffmpeg=ffmpeg version " /usr/local/share/kiln/build-verified
  grep -qx "packager_default=auto" /usr/local/share/kiln/build-verified
'

core_size="$(docker image inspect "$CORE_IMAGE" --format '{{.Size}}')"
full_size="$(docker image inspect "$FULL_IMAGE" --format '{{.Size}}')"
if [ "$core_size" -ge "$full_size" ]; then
  echo "FATAL: core image is not smaller than full ($core_size >= $full_size)" >&2
  exit 1
fi

printf 'core=%s bytes\nfull=%s bytes\n' "$core_size" "$full_size"
echo "RESULT: PASS - core/full image contracts hold."
