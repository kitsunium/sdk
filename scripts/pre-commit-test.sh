#!/usr/bin/env bash
#
# pre-commit-test.sh — run the BATS suites that guard scripts/pre-commit/.
#
# ADR 0088 measured and fixed the `… | grep -q` shape in .githooks/commit-msg
# and in the two release scripts, and recorded the sweep as INCOMPLETE in as
# many words: two scripts/pre-commit/ checks pipe into an early-exiting reader
# the same way and had not been measured. They have been now, and they fail in
# OPPOSITE directions — which is why neither was noticed by reading:
#
#   check-ktn-phases-1-7.sh:69   fails CLOSED. `printf … | grep -q 'No issues
#     found'` is the condition of an elif. grep exits on its first match, printf
#     takes EPIPE, pipefail reports 141, and 141 is false — so a linter run that
#     had just reported success was reported as "the linter did not complete".
#     Noise, not a hole: a human sees it and re-runs.
#
#   check-audit-coverage.sh:89   fails OPEN, which is worse. `grep -E … "$file"
#     | grep -qvE "$reexport"` assigns the pipeline's status to `status`, and
#     the comment above it asserted that status "is the last grep's". Under
#     pipefail it is not: `grep -qv` exits on its FIRST non-matching line, the
#     producer takes EPIPE, and 141 wins. `status` is then non-zero for a file
#     that DOES declare error codes, the file is not printed, and a package
#     whose only declaring file is that one drops out of the scan — so a package
#     absent from //:audit_sources passes the gate that exists to catch exactly
#     that. Silently.
#
# Both are reproduced by a named test below, and both were confirmed RED against
# the pre-fix form of their script — a test that has never seen red proves
# nothing. Each is paired with a negative control at the SAME shape below the
# threshold, which stayed green in both passes; that is what pins the cause to
# grep's early exit rather than to the volume of input.
#
# This runner lives in scripts/, NOT in scripts/pre-commit/, and that placement
# is load-bearing. .githooks/pre-commit:33 dispatches EVERY executable *.sh
# under scripts/pre-commit/ as a commit gate:
#
#     find "$LOCAL_DIR" -maxdepth 1 -type f -name '*.sh' -perm -u+x -print0
#
# so a runner sitting there would run bats on every commit and, on a clone
# without bats, exit 127 and block the commit outright — a test harness turned
# into a hard gate by its directory. scripts/hooks-test.sh is outside .githooks/
# for the same reason. The *.bats suite stays next to the scripts it covers,
# where it is not a *.sh and the hook never picks it up.
#
# Run: scripts/pre-commit-test.sh
#      make pre-commit-check
#
# Seconds to run, no network, no Bazel, no Go: each test builds a throwaway tree
# and asserts on the verdict the guard returns for it.

set -euo pipefail

SUITE_DIR="$(cd "$(dirname "$0")/pre-commit" && pwd)"

# Globbed, never enumerated — same reason as the release runner: a hard-coded
# list makes the Makefile target's description false the moment a second suite
# is added, and the new suite would be committed, reviewed, and executed by
# nothing. `nullglob` is off deliberately, so an empty directory yields the
# literal pattern and is caught by the existence check rather than silently
# running zero tests.
SUITES=()
for suite in "$SUITE_DIR"/*.bats; do
  SUITES+=("$suite")
done

if [ "${#SUITES[@]}" -eq 0 ] || [ ! -e "${SUITES[0]}" ]; then
  printf 'pre-commit-test: no *.bats suite found in %s\n' "$SUITE_DIR" >&2
  printf '  the runner is wired but there is nothing for it to run\n' >&2
  exit 1
fi

if ! command -v bats >/dev/null 2>&1; then
  cat >&2 <<'EOF'
pre-commit-test: bats-core is not on PATH.

The pre-commit guards' regression suite is written in BATS. Install it with one
of:

    sudo apt-get install -y bats     # Debian / Ubuntu — what CI uses
    brew install bats-core           # macOS
    npm install -g bats              # anywhere node is available

Then re-run: make pre-commit-check
EOF
  exit 127
fi

# Name the interpreter that produced the result. A suite that passes tells you
# nothing about WHICH bats ran it, and the distro package lags upstream by whole
# minor versions.
printf 'pre-commit-test: %s (%s)\n' "$(bats --version)" "$(command -v bats)"

# One bats invocation over every suite, so the TAP plan covers the whole set and
# a suite that fails to even parse is a failure rather than a silently skipped
# file.
bats "${SUITES[@]}"
