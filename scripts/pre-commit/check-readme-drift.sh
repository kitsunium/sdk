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
# `tool` directive in pkg/go.mod keeps the consumer dep graph
# clean (was 54 indirect deps, now 9).
if ! command -v gomarkdoc >/dev/null 2>&1; then
    echo "✗ gomarkdoc not on PATH. Install via:" >&2
    echo "    go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0" >&2
    echo "  (or rebuild the devcontainer to pick up the Go feature update)." >&2
    exit 1
fi

# The package list is DERIVED from the //go:generate gomarkdoc directives,
# not enumerated. It used to be enumerated, and the copies diverged: the
# Makefile regenerated 15 packages while this gate checked 8 and 27 declared a
# directive. tlsid sat in the first list and not the second, so `make
# docs-readme` would rewrite its README while no gate ever compared it — and it
# went stale unnoticed. Deriving the list makes "regenerated" and "checked" the
# same set by construction, which is the only version of this that stays true.
# (check-readme-determinism.sh keeps a small fixed sample on purpose: it tests
# gomarkdoc's own reproducibility, not per-package coverage.)
#
# Two roots publish generated READMEs: pkg/v1 and the framework module above it
# (ADR 0147). Each package is recorded as "<root> <dir>", so the check runs from
# the root its go:generate line is relative to.
#
# A read loop, not `readarray`: readarray is bash 4, and the hook runs under
# macOS's /bin/bash 3.2 as well (#260).
roots=(pkg/v1 framework)
packages=()
for root in "${roots[@]}"; do
    [ -d "$root" ] || continue
    while IFS= read -r dir; do
        [ -n "$dir" ] && packages+=("$root $dir")
    done < <(
        cd "$root" && grep -rl --include='*.go' 'go:generate gomarkdoc' . \
          | xargs -n1 dirname | sort -u
    )
done

# An empty list would make this gate pass vacuously, which is exactly the
# failure mode an allowlist-shaped check has to close explicitly.
if [ "${#packages[@]}" -eq 0 ]; then
    echo "✗ no pkg/v1 or framework package declares //go:generate gomarkdoc — refusing to pass vacuously" >&2
    exit 1
fi

# ONE INVOCATION PER PACKAGE, deliberately — not one call listing them all.
# gomarkdoc v1.1.0's exit status is unreliable when a single call checks
# several packages: with the 27 this repo declares, a real drift in tlsid was
# printed to the terminal and still exited 0 in four runs out of five. Checked
# one at a time the status is stable (8/8 both ways, drifting and clean), so
# the loop is the only thing that makes this gate mean anything. A gate that
# reports a diff and exits 0 is worse than no gate: it looks like it ran.
#
# Hard-code --repository.url + --repository.default-branch + --repository.path
# so the source-link rendering is identical between local + CI. Without
# these, gomarkdoc auto-detects from the working tree's git state (current
# branch, remote URL, default branch via `git symbolic-ref refs/remotes/origin/HEAD`)
# — which varies between a devcontainer checkout and the CI runner and
# produces a phantom drift in the link shape that this gate then flags.
drifted=()
for entry in "${packages[@]}"; do
    root="${entry%% *}"
    pkg="${entry#* }"
    if ! ( cd "$root" && gomarkdoc --check \
        --output '{{.Dir}}/README.md' \
        --repository.url 'https://github.com/kitsunium/sdk' \
        --repository.default-branch main \
        --repository.path "/$root" \
        "$pkg" ); then
        drifted+=("$root/${pkg#./}")
    fi
done

# Every stale README is named in one run rather than only the first, so a
# regeneration round does not have to be repeated package by package.
if [ "${#drifted[@]}" -ne 0 ]; then
    echo "✗ README drift in: ${drifted[*]}" >&2
    echo "  run 'make docs-readme' and commit the result" >&2
    exit 1
fi
