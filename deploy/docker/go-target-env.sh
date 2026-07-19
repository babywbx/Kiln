#!/bin/sh
# shellcheck disable=SC2317

target_arch=${1:-${TARGETARCH:-}}
target_variant=${2:-${TARGETVARIANT:-}}
if [ -z "$target_arch" ]; then
  echo "target architecture is required" >&2
  return 1 2>/dev/null || exit 1
fi

GOARCH=$target_arch
export GOARCH

case "$target_arch" in
  arm)
    case "$target_variant" in
      v6) GOARM=6 ;;
      v7) GOARM=7 ;;
      *)
        echo "unsupported ARM target variant: $target_variant" >&2
        return 1 2>/dev/null || exit 1
        ;;
    esac
    export GOARM
    ;;
  amd64)
    GOAMD64=v1
    export GOAMD64
    ;;
  arm64)
    GOARM64=v8.0
    export GOARM64
    ;;
  *)
    echo "unsupported target architecture: $target_arch" >&2
    return 1 2>/dev/null || exit 1
    ;;
esac
