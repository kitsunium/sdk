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

# gomarkdoc must be on PATH — shipped by the devcontainer Go feature
# (.devcontainer/features/languages/go/install.sh). Avoiding the
# `tool` directive in pkg/v1/go.mod keeps the consumer dep graph
# clean (was 54 indirect deps, now 9).
if ! command -v gomarkdoc >/dev/null 2>&1; then
    echo "✗ gomarkdoc not on PATH. Install via:" >&2
    echo "    go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0" >&2
    echo "  (or rebuild the devcontainer to pick up the Go feature update)." >&2
    exit 1
fi

# Explicit per-package list (NOT a glob). Git pathspec ** does not
# recurse across directories without :(glob) magic, and the explicit
# list is more readable + version-independent.
( cd pkg/v1 && gomarkdoc --check \
    --output '{{.Dir}}/README.md' \
    ./codec ./errs ./logger )
