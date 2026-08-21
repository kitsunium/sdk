#!/usr/bin/env bash
# check-alloc-lane-coverage.sh — pre-commit guard: no `!race` test runs nowhere.
#
# Rule (project policy, see CLAUDE.md §SDK-wide rules):
#   Every directory holding a `//go:build !race` *_test.go file MUST be covered
#   by an entry in tools/alloc-lane-targets.txt.
#
# Rationale:
#   The race suite (`bazel test --config=ci //...`) turns race on, so Go's build
#   constraints drop every `//go:build !race` file at compile time. Those files
#   run in exactly one place: the race-off allocation lane
#   (`bazel test --config=alloc <targets>`). A `!race` test whose package is
#   absent from that target list is executed by NO lane — it compiles, it never
#   runs, and nothing reports it as skipped. Coverage tools do not flag it
#   either, because the file is not part of the race-config build graph at all.
#
#   This is the generalised form of the exemption invariant: any test excluded
#   from normal discovery needs a NAMED, EXECUTABLE compensating gate — or an
#   explicit declaration that it is not meant to run.
#
# Exit codes:
#   0 — every `!race` test directory is covered
#   1 — at least one is orphaned

set -euo pipefail

WORKSPACE="${1:-${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}}"
cd "$WORKSPACE"

LANE_FILE="tools/alloc-lane-targets.txt"

if [ ! -f "$LANE_FILE" ]; then
    echo "✘ $LANE_FILE missing — the alloc lane has no source of truth."
    exit 1
fi

# Bazel package paths covered by the lane, derived from the target list.
# `//a/b:t`    covers exactly a/b
# `//a/b/...`  covers a/b and everything beneath it
exact_pkgs=()
prefix_pkgs=()
while IFS= read -r line; do
    line="${line%%#*}"                       # strip trailing comments
    line="$(echo "$line" | tr -d '[:space:]')"
    [ -z "$line" ] && continue
    case "$line" in
        //*/...)
            p="${line#//}"; p="${p%/...}"
            prefix_pkgs+=("$p")
            ;;
        //*:*)
            p="${line#//}"; p="${p%%:*}"
            exact_pkgs+=("$p")
            ;;
        *)
            echo "✘ $LANE_FILE: unparsable entry '$line' (expected //pkg:target or //pkg/...)"
            exit 1
            ;;
    esac
done < "$LANE_FILE"

covered() {
    local dir="$1" p
    for p in ${exact_pkgs+"${exact_pkgs[@]}"}; do
        [ "$dir" = "$p" ] && return 0
    done
    for p in ${prefix_pkgs+"${prefix_pkgs[@]}"}; do
        case "$dir" in
            "$p"|"$p"/*) return 0 ;;
        esac
    done
    return 1
}

# Every directory owning at least one `//go:build !race` test file. The
# constraint must appear in the build-constraint prologue, so grep the head of
# the file rather than the whole body (a `!race` string in prose is not a tag).
race_off_dirs=$(
    find . -name '*_test.go' -not -path './.git/*' -print0 2>/dev/null \
        | xargs -0 -r grep -l -m1 '^//go:build .*!race' 2>/dev/null \
        | xargs -r -n1 dirname \
        | sed 's|^\./||' \
        | sort -u \
        || true
)

orphans=0
while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    if ! covered "$dir"; then
        echo "✘ $dir — holds a //go:build !race test but no alloc-lane target covers it"
        echo "    → this test is executed by NO CI lane."
        orphans=1
    fi
done <<< "$race_off_dirs"

if [ "$orphans" -ne 0 ]; then
    echo ""
    echo "Pre-commit: alloc-lane coverage invariant violated."
    echo "  A //go:build !race test is invisible to the race suite, so the"
    echo "  race-off alloc lane is its ONLY gate. Add the package's go_test"
    echo "  target to $LANE_FILE (same commit), or drop the !race constraint."
    exit 1
fi

exit 0
