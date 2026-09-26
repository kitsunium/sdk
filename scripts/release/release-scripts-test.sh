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
# What the suites guard is not cosmetic. compute-bumps.sh, cut-tags.sh and
# check-pr-size.sh decide which tag every merge to main gets, and three of the
# defects they have had were invisible on review — each produced a plausible
# version rather than an error:
#
#   1. the release SIZE read out of message text. The squash message is
#      composed from contributor commits, so a `Release-bump:` trailer could be
#      buried by them (pkg/v0.1.35 and pkg/v0.3.4 shipped as patches that had
#      asked for a minor, #217) or written by them (#224). ADR 0135 moved the
#      size to a maintainer's `release:*` label on the pull request; the suites
#      drive a gh stub (test-helpers.bash) so the label lookup, a failed lookup
#      and an absent gh are each a named test.
#
#   2. a range walk without --first-parent descends into the commits a merge
#      brought IN, so a contributor's own commit sizes the release — the exact
#      smuggling ADR 0007 §2 gates against.
#
#   3. the per-commit path check without `-m --first-parent`: a true merge shows
#      NO files under a plain --name-only, so a maintainer's size on one was
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

# Globbed, never enumerated. The Makefile target and the CI step both describe
# this as the runner for `scripts/release/*.bats`, and a hard-coded list makes
# that description false the moment somebody adds a third suite: it would be
# committed, reviewed, and executed by nothing — which is the exact defect this
# whole change exists to close. `nullglob` is off deliberately, so an empty
# directory yields the literal pattern and is caught by the existence check
# below rather than silently running zero tests.
SUITES=()
for suite in "$HERE"/*.bats; do
  SUITES+=("$suite")
done

if [ "${#SUITES[@]}" -eq 0 ] || [ ! -e "${SUITES[0]}" ]; then
  printf 'release-scripts-test: no *.bats suite found in %s\n' "$HERE" >&2
  printf '  the runner is wired but there is nothing for it to run\n' >&2
  exit 1
fi

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
    printf '  the suites would SKIP their label, range-walk and merge-scoping\n' >&2
    printf '  cases and report success. A gate that skips is not a gate.\n' >&2
    exit 1
  fi
fi

# Name the interpreter that produced the result. A suite that passes tells you
# nothing about WHICH bats ran it, and the distro package lags upstream by
# whole minor versions.
printf 'release-scripts-test: %s (%s)\n' "$(bats --version)" "$(command -v bats)"

# One bats invocation over every suite, so the TAP plan covers the whole set and
# a suite that fails to even parse is a failure rather than a silently skipped
# file.
bats "${SUITES[@]}"
