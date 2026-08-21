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

if ! command -v gomarkdoc >/dev/null 2>&1; then
    echo "✗ gomarkdoc not on PATH (shipped via the devcontainer Go feature)" >&2
    exit 1
fi

a="$(mktemp -d)"
b="$(mktemp -d)"
trap 'rm -rf "$a" "$b"' EXIT

# Generate twice into temp prefixes so we never overwrite the committed
# READMEs. {{.Dir}} expands to the package's source dir; we redirect
# via cp-after-the-fact instead. Repository flags mirror
# check-readme-drift.sh so both gates agree on the URL/branch shape.
gen() {
  local out="$1"
  ( cd pkg/v1 && gomarkdoc \
      --output "${out}/{{.Dir}}/README.md" \
      --repository.url 'https://github.com/kitsunium/sdk' \
      --repository.default-branch main \
      --repository.path '/pkg/v1' \
      ./cache ./codec ./errs ./id ./logger ) >/dev/null
}

gen "$a"
gen "$b"

# Strip the absolute temp-dir prefix before diffing so the comparison
# is purely content-based.
diff -ru \
  --exclude='*.go' --exclude='*.bash' \
  "$a" "$b" \
  || { echo "[determinism] gomarkdoc produced different output across two runs" >&2; exit 1; }
