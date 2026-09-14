#!/usr/bin/env bash
#
# ci-gates-check.sh — fail when a quality gate stops being a quality gate.
#
# A Makefile target CI never runs is documentation, not a gate. This checks the
# two ways that happens, both of them silent:
#
#   1. the target is removed or renamed, and the CI step now invokes nothing;
#   2. the CI step is deleted, and the target survives as a thing people are
#      told to run by hand.
#
# The SDK had a live specimen of the second shape before this script existed,
# and it is what motivated it: scripts/release/*.bats — the regression suite
# for the two scripts that decide which tag every merge gets — was executed by
# no make target, no CI lane and no hook, from the commit that introduced it.
# ADR 0085 §Deferred records the gap; nothing enforced closing it.
#
# The manifest below includes this script's own target. A commit that drops
# ci-gates-check from CI fails ci-gates-check, for as long as it is still
# running — which is the last commit where anyone can see it.
#
# Scope, deliberately: this asserts the Makefile↔CI link for the targets in
# GATES. It does NOT audit the shell checks bazel-ci.yml invokes directly as
# `bash scripts/…`; those fail loudly by themselves when renamed (exit 127).
# The measured asymmetries that are NOT covered here are written down in
# ADR 0088 rather than silently enforced.

set -euo pipefail

cd "$(dirname "$0")/.."

# The SDK lane is bazel-ci.yml, not ci.yml: .github/workflows/CLAUDE.md makes it
# the single source of truth for gating (ADR 0004), and the other workflows are
# either release-triggered or inherited from the devcontainer template and
# path-gated on .devcontainer/**.
WORKFLOW=".github/workflows/bazel-ci.yml"
MAKEFILE="Makefile"

# Targets that MUST exist in the Makefile and MUST be invoked by the CI lane.
# Add a gate here the moment it becomes one; the point is that removing it from
# CI has to be a visible edit to this list.
GATES=(
  ci-gates-check
  release-scripts-check
)

fail=0

note() { printf '  %s\n' "$1"; }

# Every .PHONY declaration in the Makefile, backslash continuations folded in,
# flattened to a single space-separated list. Compared whole-word afterwards, so
# `release-scripts-check` can never be satisfied by a `release-scripts-check-v2`
# that happens to contain it.
phony_targets=""

if [ ! -f "$WORKFLOW" ]; then
  echo "ci-gates-check: $WORKFLOW not found"
  exit 1
fi

if [ ! -f "$MAKEFILE" ]; then
  echo "ci-gates-check: $MAKEFILE not found"
  exit 1
fi

phony_targets="$(
  awk '
    /^\.PHONY:/ { collecting = 1; sub(/^\.PHONY:/, ""); }
    collecting {
      line = $0
      cont = (line ~ /\\$/)
      sub(/\\$/, "", line)
      printf "%s ", line
      if (!cont) { collecting = 0 }
    }
  ' "$MAKEFILE" | tr -s '[:space:]' ' '
)"

if [ -z "${phony_targets// /}" ]; then
  echo "ci-gates-check: $MAKEFILE declares no .PHONY targets — has the layout changed?"
  exit 1
fi

for gate in "${GATES[@]}"; do
  if ! grep -qE "^${gate}:" "$MAKEFILE"; then
    echo "MISSING TARGET: '${gate}' is listed as a CI gate but no such target exists in $MAKEFILE"
    note "either restore the target or remove it from GATES in $0"
    fail=1
    continue
  fi

  # A target that is not .PHONY is one `touch ci-gates-check` away from being
  # skipped as up to date. These gates produce no file, so the declaration is
  # the only thing that keeps them runnable.
  #
  # Matched against the JOINED declaration, not the raw line: .PHONY is one long
  # line today, and the obvious tidy-up is to wrap it with backslashes the way
  # ktn-linter's is wrapped. A line-anchored grep would then report every gate as
  # NOT PHONY — a false red on a reformat, which is how a gate loses its
  # credibility. `phony_targets` is computed once, above the loop.
  if ! printf '%s' " $phony_targets " | grep -qF " $gate "; then
    echo "NOT PHONY: '${gate}' is a CI gate but is not declared in .PHONY in $MAKEFILE"
    note "a file of that name in the worktree would make make skip it"
    fail=1
  fi

  # `\b` after the target name is not enough on its own: `make test-race`
  # satisfies a search for `make test`, because `-` is a word boundary. The
  # gates here have no such prefix relationship, and the assertion is written
  # to reject one anyway — end of line, or a space that is not a dash.
  if ! grep -qE "make ${gate}([[:space:]]|$)" "$WORKFLOW"; then
    echo "UNGATED: 'make ${gate}' exists but $WORKFLOW never runs it"
    note "a gate CI does not run is not a gate — add the step, or remove it from GATES in $0"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "ci-gates-check: at least one quality gate is not enforced."
  exit 1
fi

printf 'All %d named gates are enforced by %s.\n' "${#GATES[@]}" "$WORKFLOW"
