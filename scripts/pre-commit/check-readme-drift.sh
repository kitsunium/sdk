#!/usr/bin/env bash
# scripts/pre-commit/check-readme-drift.sh — canonical README drift gate.
# Invoked from `make lint`, the pre-commit hook chain, and the
# `.github/workflows/bazel-ci.yml` workflow. Single source of truth for
# what "drift" means: a committed pkg/v1/<service>/README.md must match
# what `go tool gomarkdoc` would produce now from the package's doc
# comments. See ADR 0008 §Decision.
#
# Exits non-zero on any drift; prints the diff to stderr.

set -euo pipefail

cd "$(dirname "$0")/../.."

# Explicit per-package list (NOT a glob). Git pathspec ** does not
# recurse across directories without :(glob) magic, and the explicit
# list is more readable + version-independent.
( cd pkg/v1 && go tool gomarkdoc --check \
    --output '{{.Dir}}/README.md' \
    ./codec ./errs ./logger )
