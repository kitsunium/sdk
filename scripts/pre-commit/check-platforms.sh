#!/usr/bin/env bash
# scripts/pre-commit/check-platforms.sh — the platforms table and the
# cross-build matrix are one table.
#
# scripts/ci/platforms.sh prints the twelve GOOS/GOARCH cells the SDK is built
# for and judged on, and scripts/cross-platform-audit.sh loops over it. The
# `cross-build` job of .github/workflows/bazel-ci.yml compiles the same cells,
# but a job's matrix has to be written in the workflow itself. Before the table
# existed the local audit carried a copy of its own, which nothing checked
# against the lane's; the copy tools/genindex judges doc links on is held to
# the workflow by its Test_platforms — illumos and solaris were compiled by the
# lane and judged by no doc-link check until that test was written (ADR 0144).
#
# This fails when the table and the matrix differ: a cell one names and the
# other does not, or the same cells in another order — the order is the
# report's, and two orders are two tables.
#
# Usage: check-platforms.sh [<workspace>]
# Exit codes:
#   0 — the table prints exactly the matrix's cells, in its order
#   1 — they differ, or either could not be read (a guard that reads nothing
#       passes nothing)

set -euo pipefail

WORKSPACE="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$WORKSPACE"

TABLE="scripts/ci/platforms.sh"
WORKFLOW=".github/workflows/bazel-ci.yml"

for f in "$TABLE" "$WORKFLOW"; do
  if [ ! -f "$f" ]; then
    echo "check-platforms: $f not found" >&2
    exit 1
  fi
done

table=""
rc=0
table="$(bash "$TABLE")" || rc=$?
if [ "$rc" -ne 0 ]; then
  echo "check-platforms: $TABLE exited $rc — refusing to compare a table that did not print" >&2
  exit 1
fi
if [ -z "$table" ]; then
  echo "check-platforms: $TABLE printed no cell" >&2
  exit 1
fi

# The cells of the cross-build job's matrix: every `- { goos: X, goarch: Y }`
# flow mapping between the job's key and the next job key at the same indent,
# quotes dropped, written goos/goarch. awk reads to the end of the file, so no
# reader closes the pipe early.
matrix="$(
  awk '
    /^  cross-build:/                 { in_job = 1; next }
    in_job && /^  [A-Za-z0-9_-]+:/     { in_job = 0 }
    in_job && /^[[:space:]]*- \{[[:space:]]*goos:/ {
      line = $0
      gsub(/"/, "", line)
      sub(/^[[:space:]]*- \{[[:space:]]*goos:[[:space:]]*/, "", line)
      goos = line
      sub(/[[:space:]]*,.*$/, "", goos)
      sub(/^.*goarch:[[:space:]]*/, "", line)
      sub(/[[:space:]]*\}.*$/, "", line)
      print goos "/" line
    }
  ' "$WORKFLOW"
)"
if [ -z "$matrix" ]; then
  echo "check-platforms: no cell found in the cross-build matrix of $WORKFLOW — has its layout changed?" >&2
  exit 1
fi

if [ "$table" != "$matrix" ]; then
  echo "✗ $TABLE and the cross-build matrix of $WORKFLOW are not one table."
  echo ""
  echo "  $TABLE prints:"
  printf '%s\n' "$table" | sed 's/^/    /'
  echo "  the cross-build matrix lists:"
  printf '%s\n' "$matrix" | sed 's/^/    /'
  echo ""
  echo "  Edit both in the same change, in the same order: the table is what the"
  echo "  local cross-build audit loops over, the matrix what CI compiles."
  exit 1
fi

n=0
while IFS= read -r _cell; do n=$((n + 1)); done <<<"$table"
echo "check-platforms: $TABLE and the cross-build matrix name the same $n cells, in the same order"
