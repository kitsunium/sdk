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
#   Two more invariants keep the allocation gates counting one way. A gate
#   counts the allocations its window makes on its own call stack, with the
#   instrument in allocwindow_test.go: the copies of that file, one per package
#   that gates (a test helper shared across packages would be an importable,
#   designed package), must be identical below their package clause. And no
#   other hand-written test reads runtime.MemStats' Mallocs: a process-wide
#   delta also counts what other goroutines and the runtime allocate beside
#   the window, and failed the console writer's gate on a loaded runner over
#   a path with no allocation site. kit's generated files are kit's to change.
#
# Exit codes:
#   0 — every `!race` test directory is covered, and the gates count one way
#   1 — at least one is orphaned, or a gate counts another way

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

# Every copy of the allocation window is the same below its package clause:
# the body most copies carry is the reference, and each other one is named.
window_drift=0
reference=""
window_sums=$(
    find . -name 'allocwindow_test.go' -not -path './.git/*' | sed 's|^\./||' | sort \
        | while IFS= read -r copy; do
            printf '%s %s\n' "$(grep -v '^package ' "$copy" | sha256sum | cut -d' ' -f1)" "$copy"
        done
)
if [ -n "$window_sums" ]; then
    majority=$(cut -d' ' -f1 <<< "$window_sums" | sort | uniq -c | sort -rn | head -n1 | awk '{print $2}')
    reference=$(awk -v h="$majority" '$1 == h {print $2; exit}' <<< "$window_sums")
    while read -r sum copy; do
        if [ "$sum" != "$majority" ]; then
            echo "✘ $copy — differs from $reference below its package clause"
            window_drift=1
        fi
    done <<< "$window_sums"
fi

# No hand-written test counts the process's allocations: kit's generated files
# (first line `// Code generated by kit`) and the window itself are the only
# readers of runtime.MemStats' Mallocs.
global_counters=$(
    find . -name '*_test.go' -not -name 'allocwindow_test.go' -not -path './.git/*' -print0 2>/dev/null \
        | xargs -0 -r grep -l '\.Mallocs\b' 2>/dev/null \
        | while IFS= read -r f; do
            head -n1 "$f" | grep -q '^// Code generated by kit' || echo "${f#./}"
        done \
        || true
)
while IFS= read -r f; do
    [ -z "$f" ] && continue
    echo "✘ $f — reads runtime.MemStats' Mallocs, a process-wide count"
    echo "    → count the window's own stack with mallocsOver (allocwindow_test.go)."
    window_drift=1
done <<< "$global_counters"

if [ "$window_drift" -ne 0 ]; then
    echo ""
    echo "Pre-commit: allocation-window invariant violated."
    echo "  Every allocation gate counts with allocwindow_test.go, one identical"
    echo "  copy per package: copy it from $reference, change only its package"
    echo "  clause, and drop the gate's own MemStats reading."
fi

if [ "$orphans" -ne 0 ]; then
    echo ""
    echo "Pre-commit: alloc-lane coverage invariant violated."
    echo "  A //go:build !race test is invisible to the race suite, so the"
    echo "  race-off alloc lane is its ONLY gate. Add the package's go_test"
    echo "  target to $LANE_FILE (same commit), or drop the !race constraint."
fi

if [ "$orphans" -ne 0 ] || [ "$window_drift" -ne 0 ]; then
    exit 1
fi

exit 0
