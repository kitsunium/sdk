#!/usr/bin/env bash
# scripts/pre-commit/check-readme-determinism.sh — permanent CI step.
# Runs `go tool gomarkdoc` twice into separate temp dirs and diffs the
# output. A non-empty diff signals that gomarkdoc — or one of its
# template dependencies — has introduced non-determinism that would
# defeat the drift gate (check-readme-drift.sh would then become flaky).
#
# See ADR 0008 §Consequences for why this guard ships as a permanent
# step rather than a Phase-A-only hygiene check.

set -euo pipefail

cd "$(dirname "$0")/../.."

a="$(mktemp -d)"
b="$(mktemp -d)"
trap 'rm -rf "$a" "$b"' EXIT

# Generate twice into temp prefixes so we never overwrite the committed
# READMEs. {{.Dir}} expands to the package's source dir; we redirect
# via cp-after-the-fact instead.
gen() {
  local out="$1"
  ( cd pkg/v1 && go tool gomarkdoc \
      --output "${out}/{{.Dir}}/README.md" \
      ./codec ./errs ./logger ) >/dev/null
}

gen "$a"
gen "$b"

# Strip the absolute temp-dir prefix before diffing so the comparison
# is purely content-based.
diff -ru \
  --exclude='*.go' --exclude='*.bash' \
  "$a" "$b" \
  || { echo "[determinism] gomarkdoc produced different output across two runs" >&2; exit 1; }
