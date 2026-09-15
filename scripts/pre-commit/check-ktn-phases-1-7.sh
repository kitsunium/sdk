#!/usr/bin/env bash
# check-ktn-phases-1-7.sh — hard-gate against active-phase ktn-linter issues.
#
# Rule (project policy, CLAUDE.md §10):
#   Active phases 1-7 MUST report 0 issues across the whole SDK before any
#   commit lands. Phase 8 (tests) is opt-in and intentionally outside this
#   gate — its rules drive coverage uplift on a separate cadence.
#
# Why this hook (and not the PostToolUse daemon hook):
#   The ktn-linter daemon's PostToolUse blocking is hard-coded to severity
#   `error|critical` only; warnings and infos pass through silently. That
#   surfaces problems in real time but does NOT prevent them from being
#   committed. This pre-commit hook is the hard-gate.
#
# Exit codes:
#   0 — clean (0 issues in active phases 1-7)
#   1 — at least one active-phase issue present

set -euo pipefail

WORKSPACE="${1:-${CLAUDE_PROJECT_DIR:-/workspace}}"
cd "$WORKSPACE"

if ! command -v ktn-linter >/dev/null 2>&1; then
    cat >&2 <<EOF
═══════════════════════════════════════════════════════════════
  ✘ ktn-linter binary not found — active-phase gate is ENFORCED
═══════════════════════════════════════════════════════════════

The pre-commit gate fails closed when ktn-linter is missing, so a
commit cannot bypass the active-phase lint policy by simply not
installing the tool. Install it before committing:

  /ktn

  # or manually. Two corrections on the command that used to be printed
  # here, both measured: the release ships ktn-linter_<os>_<arch>.tar.gz
  # (underscores, and an archive — not a bare ktn-linter-<os>-<arch>, which
  # has never existed), and kodflow/ktn-linter is PRIVATE, so curl on a
  # release URL answers 404 whether or not the asset is there — GitHub's
  # signed redirect drops the Authorization header. \`gh release download\` is
  # the only probe that tells "no such asset" from "not authorised".
  #
  # The version is pinned to the one .github/workflows/bazel-ci.yml installs,
  # so a local verdict and the CI verdict are the same verdict. linux/darwin;
  # the windows asset is a .zip.
  asset="ktn-linter_\$(go env GOOS)_\$(go env GOARCH).tar.gz"
  gh release download v1.11.2 --repo kodflow/ktn-linter \\
      --pattern "\$asset" --dir /tmp \\
    && tar -xzf "/tmp/\$asset" -C /tmp ktn-linter \\
    && install -m 0755 /tmp/ktn-linter "\$(go env GOPATH)/bin/ktn-linter"

═══════════════════════════════════════════════════════════════
EOF
    exit 1
fi

# Run the linter on the whole tree, restricting to phases 1-7 (the active set
# documented in .ktn-linter.yaml). --phases is the upstream CLI flag exposed
# by `ktn-linter lint --help`.
linter_output="$(ktn-linter lint --phases=1,2,3,4,5,6,7 ./... 2>&1)" && linter_status=0 || linter_status=$?

# The linter exits non-zero when it finds issues AND when it fails to run at
# all, so the exit code alone cannot tell the two apart. Its output can: a run
# that completed says either "No issues found" or "Total: N".
#
# Reading the count without that check is what let a broken linter pass this
# gate silently: a binary built for an older Go release fails to scan a newer
# module, prints no footer, and the count parsed to the empty string — which
# the old default turned into zero. The gate reported success because it had
# not managed to look. It failed closed on a missing binary and open on a
# broken one, which is the wrong way round: a tool that cannot run is the case
# where a human most needs to be told.
issue_count="$(printf '%s\n' "$linter_output" | grep -oE 'Total:[[:space:]]+[0-9]+' | awk '{print $2}' | tail -1 || true)"

# "No issues found" is only trusted alongside a zero exit: the linter can print
# it and then fail during a later phase, and the message alone would let that
# partial run stand as a clean verdict — the same failure mode this guard exists
# to close, one step further along.
# A `case` rather than `printf … | grep -q 'No issues found'`. `grep -q` exits
# on its FIRST match, the writing printf takes EPIPE, and `set -o pipefail`
# (line 19) reports 141 for input that DID contain the string — so the elif
# below evaluated FALSE on a clean run and the gate dropped into the `else`,
# failing closed on a linter that had just reported success. Measured on this
# exact shape, 40 trials per cell, marker placed FIRST in the output:
#
#       8 000 B →  0/40        96 000 B → 24/40
#      64 000 B →  4/40       400 000 B → 40/40
#
# and with the same marker placed LAST instead, 0/40 at every size — the
# negative control that pins the cause to grep's early exit rather than to the
# volume. `case` is a shell builtin: no pipe, no subprocess, no bet on a size.
clean_report=0
case "$linter_output" in
    *'No issues found'*) clean_report=1 ;;
esac

if [ -n "$issue_count" ]; then
    : # a count was reported: the run completed and the verdict is known
elif [ "$clean_report" -eq 1 ] && [ "$linter_status" -eq 0 ]; then
    exit 0
else
    cat >&2 <<EOF
═══════════════════════════════════════════════════════════════
  ✘ ktn-linter did not complete — gate FAILS CLOSED
═══════════════════════════════════════════════════════════════
The linter exited $linter_status and printed neither "No issues found" nor a
"Total: N" footer, so its verdict is unknown. This is not the same as
zero issues, and the gate refuses rather than assume.

A common cause is a linter binary built against an older Go release than
the module it is asked to scan. Check with:
  ktn-linter --version && go list -m -f '{{.GoVersion}}'
═══════════════════════════════════════════════════════════════
EOF
    printf '%s\n' "$linter_output" >&2
    exit 1
fi

issue_count="${issue_count:-0}"

if [ "$issue_count" -eq 0 ]; then
    exit 0
fi

cat >&2 <<EOF
═══════════════════════════════════════════════════════════════
  ✘ ktn-linter active-phase gate — $issue_count issue(s) in phases 1-7
═══════════════════════════════════════════════════════════════

EOF
printf '%s\n' "$linter_output" >&2
cat >&2 <<EOF

═══════════════════════════════════════════════════════════════
  Active phases 1-7 MUST be at 0 issues for the commit to land.
  Fix the issues above (or scope an exception via .ktn-linter.yaml
  per-rule \`exclude:\` if you have a documented design reason)
  and re-commit.

  To audit interactively:  ktn-linter lint --phases=1-7 ./...
  To autofix where possible: ktn-linter lint --fix ./...
═══════════════════════════════════════════════════════════════
EOF

exit 1
