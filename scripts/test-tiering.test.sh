#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

normal=$(cd "$ROOT" && go test ./modules/resources ./modules/packager/cmaf ./modules/packager/mpd -list '^(Test|Fuzz)')
if printf '%s\n' "$normal" | grep -Eq 'ResourceMatrix|Fractional|AllocationBudget|^Fuzz'; then
  echo 'normal tests include an extended resource, allocation, or fuzz check' >&2
  exit 1
fi

extended=$(cd "$ROOT" && go test -tags=extended ./modules/resources ./modules/packager/cmaf ./modules/packager/mpd -list '^(Test|Fuzz)')
for name in TestResolveAutoResourceMatrix TestResolveAutoUsesFractionalCPUQuotaWithoutEnteringFastPathEarly TestDecryptOwnedReservedStaysWithinAllocationBudget FuzzAvailableSegments; do
  if ! printf '%s\n' "$extended" | grep -q "^$name$"; then
    echo "extended tests do not include $name" >&2
    exit 1
  fi
done

echo 'test tiering checks passed'
