#!/usr/bin/env bash
# kit-coverage.sh — how much of the SDK its design rebuilds (`make
# kit-coverage`; ADR 0169): per package, the exported declarations kit writes
# (decl_gen.go's types and wrappers, facade_gen.go's re-exports, codes_gen.go's
# sentinels, design_gen.go's ports) against those written by hand, read by
# `kit design coverage` from the files alone, then the total and the packages
# with the most declared by hand.
#
# A report, not a gate: it judges nothing and exits 0 whatever it counts. It
# needs kit at the version design/sdk.yaml pins, so it is local, like `make
# regen`.
#
# Runs under macOS's /bin/bash 3.2 and the BSD tools (scripts/CLAUDE.md).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
# shellcheck source=scripts/kit-pinned.sh
. scripts/kit-pinned.sh
kit_pinned kit-coverage

table="$(mktemp "${TMPDIR:-/tmp}/kit-coverage.XXXXXX")"
trap 'rm -f "$table"' EXIT
kit design coverage . >"$table"
cat "$table"

# The ten packages with the most exported declarations still written by hand:
# where the next conversion would move the most.
printf '\nmost declared by hand:\n'
awk 'NR > 1 && $1 != "total" { print $4, $2, $1 }' "$table" |
  LC_ALL=C sort -k1,1nr -k3,3 |
  # awk reads the whole stream: head would close it early, and sort, sent
  # SIGPIPE, would fail the report under pipefail.
  awk 'NR <= 10' |
  awk '{ printf "  %6d of %6d  %s\n", $1, $2, $3 }'
