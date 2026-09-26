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
# The manifest below includes this script's own target, but be precise about what
# that buys. Deleting ANOTHER gate's step is caught: this script still runs and
# reports it UNGATED. Renaming or deleting a gate's TARGET is caught: the step
# still runs and make fails on it. Deleting THIS script's own step is NOT caught
# by this script — nothing that has been removed can report its own removal.
# That last case is branch protection's job, and as measured when this was
# written `main` requires only the `bazel` check, so it is currently nobody's.
# Recorded under ADR 0088 Deferred rather than overstated here.
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
  hooks-check
  pre-commit-check
  # `gofumpt -l` + sdkguard: two of the three checks of `make lint` that no
  # lane ran until #236. The other five are invoked by the workflow as direct
  # `bash …` steps, so they are outside this list by construction.
  #
  # `lint-ktn-check` is the third, and it is deliberately NOT listed. Its step
  # is conditional on a KTN_LINTER_TOKEN secret, because the ktn-linter release
  # lives in a private repository that a workflow token cannot read — measured
  # on this lane: `gh release download v1.11.2 --repo kodflow/ktn-linter`
  # answers `release not found` under `secrets.GITHUB_TOKEN`, and downloads the
  # asset under a credentialed account. A step a missing secret can skip is not
  # a gate, and this file must not certify one as if it were. Add it here on
  # the same commit that makes the step unconditional (#236).
  lint-check
  # scripts/ci/*.bats — the module census and the govulncheck gate (ADR 0136,
  # ADR 0137). A suite nothing runs is not a test suite (ADR 0088).
  ci-scripts-check
  # govulncheck over every module, in the required `bazel` job (#210, ADR
  # 0136). `vuln-install` is its installer, not a gate, and is not listed.
  vuln-check
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

# Every command the workflow actually EXECUTES: the value of each `run:` key,
# plus the body of each `run: |` block scalar, with comment-only lines dropped.
# Anything outside a run: — step names, `name:` prose, the long justification
# comments this repository writes above each gate — is not a command and must
# not be able to satisfy enforcement.
ci_run_commands="$(
  awk '
    {
      line = $0
      indent = match(line, /[^ ]/) - 1
      if (indent < 0) { indent = 0 }

      if (in_run) {
        if (line ~ /^[[:space:]]*$/) { next }
        if (indent > run_indent) {
          body = line
          sub(/^[[:space:]]+/, "", body)
          if (body !~ /^#/) { print body }
          next
        }
        in_run = 0
      }

      if (line ~ /^[[:space:]]*(- )?run:/) {
        value = line
        sub(/^[[:space:]]*(- )?run:[[:space:]]*/, "", value)
        run_indent = indent
        in_run = 1
        # `run: |` and `run: >` carry the command in the block below, not here.
        if (value != "" && value !~ /^[|>]/) { print value }
      }
    }
  ' "$WORKFLOW"
)"

if [ -z "${ci_run_commands//[[:space:]]/}" ]; then
  echo "ci-gates-check: $WORKFLOW declares no run: commands — has the layout changed?"
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
  #
  # A `case` rather than `printf … | grep -qF`: that pipeline is the construct
  # that classified 19 `feat:` commits as patches in the sibling repository —
  # `grep -q` closes the pipe on its first match, the writer takes EPIPE, and
  # `pipefail` reports 141 for input that DID match. It needs an input past the
  # pipe buffer to bite, which a .PHONY line is not; writing it anyway would be
  # betting on a size. `case` is a shell builtin: no pipe, no subprocess, no bet.
  case " $phony_targets " in
    *" $gate "*) ;;
    *)
      echo "NOT PHONY: '${gate}' is a CI gate but is not declared in .PHONY in $MAKEFILE"
      note "a file of that name in the worktree would make make skip it"
      fail=1
      ;;
  esac

  # Matched against the EXECUTABLE `run:` commands only, never the raw file.
  # Grepping the whole YAML accepts a gate that exists solely in a comment: the
  # step gets deleted, the explanatory comment above it survives naming the
  # command, and the check stays green over a gate that no longer runs. That is
  # the exact failure this script exists to catch, so it must not be the way the
  # script itself fails.
  #
  # It must also be at a COMMAND position — start of the command, or after a
  # `;`/`&&`/`||`/`|`/`(`. Otherwise `run: echo "we used to run make <gate>"`
  # satisfies enforcement with an echo.
  if ! grep -qE "(^|[;&|(][[:space:]]*)make ${gate}([[:space:]]|$)" <<<"$ci_run_commands"; then
    echo "UNGATED: 'make ${gate}' exists but $WORKFLOW never runs it"
    note "a gate CI does not run is not a gate — add the step, or remove it from GATES in $0"
    note "comments and echoed text do not count; it must be a run: command"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "ci-gates-check: at least one quality gate is not enforced."
  exit 1
fi

printf 'All %d named gates are enforced by %s.\n' "${#GATES[@]}" "$WORKFLOW"
