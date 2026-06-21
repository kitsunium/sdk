#!/usr/bin/env bash
# scripts/pre-commit/check-error-codes-drift.sh — error-code registry drift gate.
# The committed docs/error-codes.yaml is the human-readable mirror of the
# dotted-quad registry (ADR 0005/0006); it MUST match what
# scripts/gen-error-codes.sh produces from the errs.Code constants now. This
# guard regenerates into a temp file and diffs, so a new/changed code that
# wasn't re-exported to the YAML fails the commit (CLAUDE.md rule 11). The
# executable source of truth remains the AST audit
# (internal/kernel/errs/registry_external_test.go); this only keeps the doc
# honest.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

committed="docs/error-codes.yaml"
if [ ! -f "$committed" ]; then
	echo "✗ $committed missing — run 'make error-codes'." >&2
	exit 1
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Regenerate to a temp by pointing the generator's output at it via a copy of
# the logic: simplest is to run the generator then compare, restoring after.
cp "$committed" "$tmp"
bash scripts/gen-error-codes.sh >/dev/null

if ! diff -u "$tmp" "$committed" >/dev/null 2>&1; then
	echo "✗ docs/error-codes.yaml is stale — error codes changed but the YAML" >&2
	echo "  mirror was not regenerated. Run 'make error-codes' and commit." >&2
	diff -u "$tmp" "$committed" >&2 || true
	# Restore the committed copy so the working tree is unchanged on failure.
	cp "$tmp" "$committed"
	exit 1
fi
