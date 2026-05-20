#!/usr/bin/env bash
# check-pkg-docs.sh — pre-commit guard: every Go package MUST be documented.
#
# Rule (project policy, see CLAUDE.md §SDK-wide rules):
#   For every directory containing at least one .go file under internal/* or
#   pkg/*, that directory MUST contain CLAUDE.md or README.md (or both).
#   Public packages (pkg/v1/**) MUST additionally contain README.md — that's
#   the consumer-facing surface and is non-negotiable post-v1.0.0.
#
# Rationale:
#   - CLAUDE.md is the agent-facing operating manual (warmup hierarchy).
#   - README.md is the consumer-facing usage doc (pkg.go.dev renders it).
#   - The two roles do not collapse; private packages can ship just CLAUDE.md
#     because they have no external consumer.
#
# This check runs as part of the project-local pre-commit chain. It only
# inspects files that exist on disk at HEAD or in the index; it does NOT
# walk the worktree blindly.
#
# Exit codes:
#   0 — all packages documented
#   1 — at least one violation

set -euo pipefail

WORKSPACE="${1:-${CLAUDE_PROJECT_DIR:-/workspace}}"
cd "$WORKSPACE"

missing_any=0
missing_pub_readme=0
missing_pub_claude=0

# Discover every directory that owns at least one production *.go file.
# `_test.go` alone does not count — tests can sit in a package whose
# production files live elsewhere (build-tag-separated test fixtures).
go_dirs=$(find internal pkg -type f -name '*.go' ! -name '*_test.go' -print0 2>/dev/null \
    | xargs -0 -n1 dirname \
    | sort -u \
    || true)

while IFS= read -r dir; do
    [ -z "$dir" ] && continue

    has_claude=0
    has_readme=0
    [ -f "$dir/CLAUDE.md" ] && has_claude=1
    [ -f "$dir/README.md" ] && has_readme=1

    if [ "$has_claude" -eq 0 ] && [ "$has_readme" -eq 0 ]; then
        echo "✘ $dir — missing CLAUDE.md AND README.md"
        missing_any=1
    fi

    # Public packages (pkg/v1/**, pkg/v2/**, …) additionally require BOTH
    # README.md (consumer-facing, pkg.go.dev renders it) AND CLAUDE.md
    # (agent-facing, warmup hierarchy). The two roles do not collapse —
    # README is what humans read on pkg.go.dev, CLAUDE.md is what the
    # warmup walks. The pkg/ root itself and pkg/<major>/ index are
    # exempt because they only carry CLAUDE.md indices, not Go code; the
    # loop already filtered to dirs with .go files, so this exemption is
    # implicit.
    case "$dir" in
        pkg/v[0-9]*/*)
            if [ "$has_readme" -eq 0 ]; then
                echo "✘ $dir — public package missing README.md (consumer-facing required)"
                missing_pub_readme=1
            fi
            if [ "$has_claude" -eq 0 ]; then
                echo "✘ $dir — public package missing CLAUDE.md (agent-facing required)"
                missing_pub_claude=1
            fi
            ;;
    esac
done <<< "$go_dirs"

if [ "$missing_any" -ne 0 ] || [ "$missing_pub_readme" -ne 0 ] || [ "$missing_pub_claude" -ne 0 ]; then
    echo ""
    echo "Pre-commit: package documentation policy violated."
    echo "  - internal/* packages must have CLAUDE.md OR README.md."
    echo "  - pkg/v*/** public packages must have BOTH README.md (consumer-facing)"
    echo "    AND CLAUDE.md (agent-facing) — the two roles do not collapse."
    echo "Fix the missing files and re-commit."
    exit 1
fi

exit 0
