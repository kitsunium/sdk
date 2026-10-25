#!/usr/bin/env bash
# scripts/ci/net-gate-verdict.sh — the verdict of the `net-kernel-gate` job,
# the one status a ruleset requires of net-kernel-gate.yml.
#
# It reads the results of the jobs it needs, as GitHub reports them
# (success, failure, cancelled, skipped):
#
#   - the path filter must have succeeded: without it there is no verdict;
#   - when it found no network path changed, the lanes and the bench were
#     skipped and the gate PASSES — a skip counts as success, by design;
#   - otherwise the lanes and the bench must both have succeeded: a skipped,
#     failed or cancelled one fails the gate.
#
# Usage: net-gate-verdict.sh CHANGES_RESULT RELEVANT LANES_RESULT BENCH_RESULT
# Exit 0 when the gate passes, 1 when it does not, 64 on bad usage.

set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 CHANGES_RESULT RELEVANT LANES_RESULT BENCH_RESULT" >&2
  exit 64
fi
changes="$1" relevant="$2" lanes="$3" bench="$4"

if [ "$changes" != success ]; then
  echo "net-kernel-gate: the path filter ended '$changes': no verdict without it" >&2
  exit 1
fi
case "$relevant" in
  false)
    echo "net-kernel-gate: no network path changed; lanes '$lanes', bench '$bench' — skipped, counted as success"
    exit 0
    ;;
  true) ;;
  *)
    echo "net-kernel-gate: the path filter answered '$relevant', neither true nor false" >&2
    exit 1
    ;;
esac
fail=0
if [ "$lanes" != success ]; then
  echo "net-kernel-gate: the kernel lanes ended '$lanes'" >&2
  fail=1
fi
if [ "$bench" != success ]; then
  echo "net-kernel-gate: the bench ended '$bench'" >&2
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "net-kernel-gate: the kernel lanes and the bench passed"
