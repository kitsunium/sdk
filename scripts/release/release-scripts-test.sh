#!/usr/bin/env bash
#
# release-scripts-test.sh — run the BATS suites that guard the release scripts.
#
# scripts/release/*.bats has existed since ADR 0085 and NOTHING executed it:
# no make target, no CI lane, no hook. ADR 0085 §Deferred says so in as many
# words — "the regression tests added here were run locally with bats-core
# v1.11.1. Wiring a lane touches bazel-ci.yml and is left to a change that owns
# that file." This is that change.
#
# What the suites guard is not cosmetic. compute-bumps.sh and cut-tags.sh
# decide which tag every merge to main gets, and three of their defects are
# invisible on review — they produce a plausible version rather than an error:
#
#   1. `separator=,` in a %(trailers:…) format. git parses the trailer OPTION
#      LIST on commas, so the comma is eaten as the list delimiter and the
#      separator is left EMPTY. Two repeated trailers are then concatenated
#      into one field. Measured on git 2.47.3:
#
#        $ git log --format="%H %(trailers:key=Release-bump,valueonly,separator=,)"
#        <sha> minor          # from `Release-bump: mi` + `Release-bump: nor`
#
#        $ git log --format="%H %(trailers:key=Release-bump,valueonly,separator=%x1F)"
#        <sha> mi^_nor        # the two values, kept apart
#
#      The first cuts a MINOR release from two halves of a word. Nothing in the
#      commit, the diff or the run log says so.
#
#   2. a range walk without --first-parent descends into the commits a merge
#      brought IN, so a contributor's own `Release-bump: minor` sizes the
#      release — the exact smuggling ADR 0007 §2 gates against.
#
#   3. the per-commit path check without `-m --first-parent`: a true merge shows
#      NO files under a plain --name-only, so a maintainer's trailer on one was
#      scoped against an empty list and counted for nothing.
#
# Each is reproduced by a named test, and each test was confirmed to go RED
# against the pre-fix form of the script — a test that has never seen red
# proves nothing.
#
# Run: scripts/release/release-scripts-test.sh
#      make release-scripts-check
#
# bats is NOT vendored and this script does NOT fetch it. `make lint` already
# refuses to depend on network egress (see the guard target), and a gate that
# clones a third-party repository on every run is a flake, not a gate. CI
# installs bats from the distro package in the same step that runs this script;
# locally, any of the routes printed below works.
#
# Seconds to run, no network, no Bazel, no Go: each test builds a throwaway
# git repository and asserts on the tag the scripts compute for it.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

SUITES=(
  "$HERE/test-compute-bumps.bats"
  "$HERE/test-cut-tags.bats"
)

if ! command -v bats >/dev/null 2>&1; then
  cat >&2 <<'EOF'
release-scripts-test: bats-core is not on PATH.

The release scripts' regression suite is written in BATS. Install it with one
of:

    sudo apt-get install -y bats     # Debian / Ubuntu — what CI uses
    brew install bats-core           # macOS
    npm install -g bats              # anywhere node is available

Then re-run: make release-scripts-check
EOF
  exit 127
fi

# The suites call `skip` when `go` or `jq` is missing, because a developer
# without them should still get the assertions that do not need them. In CI that
# rule inverts: a run where the central cases SKIPPED is a green gate that tested
# nothing, which is the exact shape this lane exists to abolish. So under CI the
# prerequisites are asserted, loudly, before a single test runs.
#
# Measured on the first run of this lane: ubuntu-latest carries both, and the
# suite reported 32 ok / 0 not ok / 1 skip — the one skip being the bazel rdeps
# path, which is a different axis. This check is what keeps that true if the
# runner image changes.
if [ -n "${CI:-}" ]; then
  missing=""
  for tool in go jq; do
    command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
  done
  if [ -n "$missing" ]; then
    printf 'release-scripts-test: missing under CI:%s\n' "$missing" >&2
    printf '  the suites would SKIP their trailer, range-walk and merge-scoping\n' >&2
    printf '  cases and report success. A gate that skips is not a gate.\n' >&2
    exit 1
  fi
fi

# Name the interpreter that produced the result. A suite that passes tells you
# nothing about WHICH bats ran it, and the distro package lags upstream by
# whole minor versions.
printf 'release-scripts-test: %s (%s)\n' "$(bats --version)" "$(command -v bats)"

for suite in "${SUITES[@]}"; do
  if [ ! -f "$suite" ]; then
    printf 'release-scripts-test: missing suite %s\n' "$suite" >&2
    printf '  either restore it or drop it from SUITES in %s\n' "$0" >&2
    exit 1
  fi
done

# One bats invocation over every suite, so the TAP plan covers the whole set and
# a suite that fails to even parse is a failure rather than a silently skipped
# file.
bats "${SUITES[@]}"
