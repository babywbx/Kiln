#!/usr/bin/env bash
set -euo pipefail

url="${1:-http://127.0.0.1:8080/admin}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

run_audit() {
  local mode="$1"
  shift
  local report="$work/$mode.json"
  for attempt in 1 2 3; do
    pnpm dlx lighthouse@13.4.1 "$url" \
      --only-categories=performance,accessibility,best-practices \
      --output=json \
      --output-path="$report" \
      --chrome-flags='--headless --no-sandbox --disable-gpu' \
      --quiet \
      "$@"
    local scores
    scores="$(jq -r --arg mode "$mode" '
      "\($mode): performance=\(.categories.performance.score * 100 | floor) " +
      "accessibility=\(.categories.accessibility.score * 100 | floor) " +
      "best-practices=\(.categories["best-practices"].score * 100 | floor)"
    ' "$report")"
    if jq -e '
      .categories.performance.score == 1 and
      .categories.accessibility.score == 1 and
      .categories["best-practices"].score == 1
    ' "$report" >/dev/null; then
      echo "$scores"
      return 0
    fi
    echo "$scores (attempt $attempt/3)" >&2
  done
  return 1
}

run_audit mobile
run_audit desktop --preset=desktop
