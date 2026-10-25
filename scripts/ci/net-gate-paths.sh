#!/usr/bin/env bash
# scripts/ci/net-gate-paths.sh — the path filter of net-kernel-gate.yml: does
# the commit being evaluated touch the network engines, their kernel, their
# design or the gate itself?
#
# It compares BASE with EVAL, the two commits the event names: on a pull
# request the base branch's tip and the merge commit GitHub evaluates, in the
# merge queue the queue's base_sha and its head_sha — never the pull request's
# own base, which the queue has moved past. Prints `relevant=true` or
# `relevant=false` on stdout (a line for $GITHUB_OUTPUT), and on stderr every
# changed path that matched.
#
# Fail-closed: a commit git cannot resolve, or a diff that fails, exits 1 —
# never `relevant=false`, which would skip the gate and report success.
#
# Usage: net-gate-paths.sh BASE EVAL
# Exit 0 with the answer, 1 when git cannot answer, 64 on bad usage.

set -euo pipefail

if [ "$#" -ne 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
  echo "usage: $0 BASE EVAL" >&2
  exit 64
fi
base="$1"
eval_sha="$2"

# The paths the gate answers for, as extended regular expressions anchored at
# the repository root: the engines and their core, the kernel layer they sit
# on, the façade, the design, the toolchain lines, and the gate's own files.
patterns=(
  '^internal/service/net/'
  '^internal/core/net/'
  '^internal/kernel/'
  '^pkg/v1/net/'
  '^design/net\.yaml$'
  '^tools/netgate/'
  '^go\.(mod|work)$'
  '^internal/go\.mod$'
  '^\.github/workflows/net-kernel-gate\.yml$'
  '^scripts/ci/(net-gate-paths|net-gate-verdict|net-veth|boot-guest)\.sh$'
)

for c in "$base" "$eval_sha"; do
  if ! git rev-parse --verify --quiet "$c^{commit}" >/dev/null; then
    echo "net-gate-paths: commit $c is not in this clone (fetch-depth 0 is required)" >&2
    exit 1
  fi
done

# --no-renames: a rename lists its source too, so a file moved out of a
# matched path still counts as a change to it.
if ! changed="$(git diff --no-renames --name-only "$base" "$eval_sha")"; then
  echo "net-gate-paths: git diff $base $eval_sha failed" >&2
  exit 1
fi

regex="$(IFS='|'; echo "${patterns[*]}")"
matched="$(grep -E "$regex" <<<"$changed" || true)"
if [ -n "$matched" ]; then
  echo "net-gate-paths: the evaluated commit touches:" >&2
  while IFS= read -r path; do
    printf '  %s\n' "$path" >&2
  done <<<"$matched"
  echo "relevant=true"
else
  echo "net-gate-paths: no path of the network engines changed" >&2
  echo "relevant=false"
fi
