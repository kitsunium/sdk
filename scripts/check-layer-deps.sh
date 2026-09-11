#!/usr/bin/env bash
# scripts/check-layer-deps.sh — the layer firewall, checked on the build graph.
#
# ADR 0004 put the dependency direction kernel → core → service → pkg/v1 on
# Bazel visibility: each layer's targets visible only to the layers above it,
# so "a rogue import fails `bazel build`". Gazelle does not keep that promise.
# Every go_library under an internal/ directory gets //:__subpackages__ added
# to its visibility — Gazelle's mirror of Go's own internal/ rule — and that
# label admits the WHOLE repository. A kernel package importing core, or core
# importing service, therefore builds; ADR 0068 records it being demonstrated.
#
# So the direction is asserted on the dependency graph itself. Each query below
# lists the go_library targets one layer reaches outside what it may depend
# on, and every one must come back empty. It needs Bazel, which is why it is
# wired into `make lint` and CI rather than into the per-commit hook, whose
# guards run without it.
#
# It fails CLOSED: a query that cannot be answered stops it, because a
# firewall check that passes on an unreadable graph checks nothing.
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$root"

failed=0

# check NAME QUERY — report every target QUERY returns; there should be none.
check() {
	local name=$1 query=$2 out
	if ! out="$(bazel query --noshow_progress "$query" 2>/dev/null)"; then
		echo "✗ $name: bazel query failed — refusing to pass on a graph it could not read." >&2
		exit 1
	fi
	if [ -n "$out" ]; then
		echo "✗ $name — reached:" >&2
		while IFS= read -r target; do
			echo "    $target" >&2
		done <<<"$out"
		failed=1
	fi
}

check "kernel depends on kernel only" \
	'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'
check "core depends on no service, pkg or third-party code" \
	'kind("go_library", deps(//internal/core/...)) intersect (//internal/service/... + //pkg/... + //third-party/...)'
check "service depends on no pkg or third-party code" \
	'kind("go_library", deps(//internal/service/...)) intersect (//pkg/... + //third-party/...)'
check "pkg depends on no third-party code" \
	'kind("go_library", deps(//pkg/...)) intersect //third-party/...'

if [ "$failed" -ne 0 ]; then
	echo "The direction is kernel → core → service → pkg/v1, with third-party/ above all (ADR 0004, ADR 0068)." >&2
	exit 1
fi
echo "✓ layer direction holds: kernel → core → service → pkg/v1, third-party/ above all"
