#!/bin/sh
# shellcheck disable=SC1090
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
target_script="$script_dir/go-target-env.sh"

(
  TARGETARCH=arm TARGETVARIANT=v6
  export TARGETARCH TARGETVARIANT
  . "$target_script"
  test "$GOARCH" = arm
  test "$GOARM" = 6
)

(
  TARGETARCH=arm TARGETVARIANT=v7
  export TARGETARCH TARGETVARIANT
  . "$target_script"
  test "$GOARCH" = arm
  test "$GOARM" = 7
)

(
  TARGETARCH=amd64 TARGETVARIANT=
  export TARGETARCH TARGETVARIANT
  . "$target_script"
  test "$GOARCH" = amd64
  test "$GOAMD64" = v1
)

(
  TARGETARCH=arm64 TARGETVARIANT=v8
  export TARGETARCH TARGETVARIANT
  . "$target_script"
  test "$GOARCH" = arm64
  test "$GOARM64" = v8.0
)

(
  unset TARGETARCH TARGETVARIANT 2>/dev/null || true
  ! sh "$target_script" 2>/dev/null
)
