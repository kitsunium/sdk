#!/usr/bin/env bats
# BATS tests for the pre-commit guards that pipe into an early-exiting reader.
#
# Both cases below were found by finishing the sweep ADR 0088 opened and
# recorded as INCOMPLETE: the former commit-msg hook was measured and fixed there,
# but two `scripts/pre-commit/` checks pipe into `grep -q` the same way and had
# not been. They fail in OPPOSITE directions, which is why both are covered:
#
#   check-ktn-phases-1-7.sh  fails CLOSED — a clean linter run is reported as
#                            "the linter did not complete".
#   check-audit-coverage.sh  fails OPEN   — a package that declares error codes
#                            and is absent from //:audit_sources passes.
#
# The mechanism is one line long in both: `grep -q` / `grep -qv` exits on its
# first match, the writer on the left of the pipe takes EPIPE, and `set -o
# pipefail` reports 141 — a status the surrounding code reads as "no match".
#
# Both tests were confirmed RED against the pre-fix form of their script. The
# fixture sizes below are the measured thresholds, not guesses; the per-cell
# measurements are recorded in the comment above each fix.

setup() {
  SCRIPTS="$(cd "${BATS_TEST_DIRNAME}" && pwd)"
  WORK="$(mktemp -d)"
  cd "$WORK"
}

teardown() {
  [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

# --- check-ktn-phases-1-7.sh ------------------------------------------------

# A clean run whose output is large enough to outlast grep's early exit. The
# marker is placed FIRST because that is the cell that fires: grep -q closes
# the pipe as soon as it sees it, with the rest still unwritten.
@test "ktn-phases: a clean linter run is not reported as a failed run" {
  mkdir -p bin ws
  {
    echo '#!/usr/bin/env bash'
    echo "echo 'No issues found'"
    # 400 000 bytes after the marker: measured 40/40 failures pre-fix.
    echo "head -c 400000 /dev/zero | tr '\\0' 'x'"
    echo 'exit 0'
  } >bin/ktn-linter
  chmod +x bin/ktn-linter

  PATH="$WORK/bin:$PATH" run "$SCRIPTS/check-ktn-phases-1-7.sh" "$WORK/ws"

  # Pre-fix this is status 1 with "gate FAILS CLOSED" on stderr, because the
  # pipeline returned 141 and the elif went false.
  [ "$status" -eq 0 ]
  [[ "$output" != *"did not complete"* ]]
}

# The negative control. Same size, marker LAST: grep reads to the end, nothing
# takes EPIPE, and the pre-fix script already passed here. Keeping it makes the
# test above diagnostic rather than merely red — if both went red, the cause
# would be the volume, not the early exit.
@test "ktn-phases: negative control — marker last, same volume, already passed" {
  mkdir -p bin ws
  {
    echo '#!/usr/bin/env bash'
    echo "head -c 400000 /dev/zero | tr '\\0' 'x'"
    echo "echo 'No issues found'"
    echo 'exit 0'
  } >bin/ktn-linter
  chmod +x bin/ktn-linter

  PATH="$WORK/bin:$PATH" run "$SCRIPTS/check-ktn-phases-1-7.sh" "$WORK/ws"

  [ "$status" -eq 0 ]
}

# --- check-audit-coverage.sh ------------------------------------------------

# Builds a root where exactly one package declares error codes and is NOT in
# //:audit_sources. The guard must refuse. Pre-fix it passes, because the file
# that declares them is dropped by a 141 and the package looks empty.
#
# $1 = number of re-export lines after the declaring line.
mkroot() {
  mkdir -p internal/core/widget pkg third-party
  # One real declaration FIRST — the line `grep -qv` stops on — then enough
  # re-export lines to outrun the pipe. 200 lines measured 40/40 pre-fix;
  # 100 measured 0/40, so the fixture sits above the threshold on purpose.
  {
    echo 'package widget'
    printf '\tCodeOwn errs.Code = 42\n'
    for _ in $(seq 1 "$1"); do printf '\tCodeReexport errs.Code = core.Thing\n'; done
  } >internal/core/widget/codes.go
  # A filegroup entry must exist or the guard stops on "lists no audit_srcs
  # entry" — which would make the test green for the wrong reason. It names a
  # DIFFERENT package, so internal/core/widget is genuinely uncovered.
  echo 'filegroup(name = "audit_sources", srcs = ["//internal/kernel/errs:audit_srcs"])' >BUILD.bazel
}

@test "audit-coverage: an uncovered package with a large codes.go is still caught" {
  mkroot 400

  run "$SCRIPTS/check-audit-coverage.sh" "$WORK"

  # Pre-fix: status 0, no output — the gap passes.
  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/widget"* ]]
}

# The negative control for the same guard: identical shape, small enough that
# the writer finishes before grep exits. Pre-fix AND post-fix this is red-for-
# the-right-reason, i.e. the guard catches the gap. If this one ever flips, the
# cause is the guard's logic, not the pipe.
@test "audit-coverage: negative control — same shape under the threshold" {
  mkroot 20

  run "$SCRIPTS/check-audit-coverage.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/widget"* ]]
}

# --- portability (#260) -----------------------------------------------------

# Every guard must work with the tools a Mac ships: /bin/bash 3.2 and the BSD
# find in /usr/bin, because `make lint` runs them on a developer's machine as
# well as in CI. CI runs bash 5 and GNU find, where none of the constructs below
# fails, so no run there can notice one coming back; reading the sources can.
# The pattern names what broke the guards before #260 — readarray / mapfile and
# associative arrays (bash 4), the case-changing expansions (bash 4) and
# `find -printf` (GNU) — which is a narrower claim than "portable", and the
# portability itself was proven by running the guards on both. A match on a
# comment line is ignored: the guards say what they avoid.
#
# Confirmed RED against the pre-fix scripts: it named
# check-domain-docs.sh:38 (-printf) and check-readme-drift.sh:35 (readarray).
@test "portability: no guard needs bash 4 or GNU find's -printf" {
  root="$(cd "$SCRIPTS/../.." && pwd)"
  pattern='readarray|mapfile|(declare|local|typeset) -[a-zA-Z]*A|\$\{[A-Za-z_][A-Za-z0-9_]*(,,?|\^\^?)[^}]*\}|-printf'
  hits="$(grep -nE "$pattern" \
      "$SCRIPTS"/*.sh "$root/scripts/gen-error-codes.sh" \
    | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)"
  if [ -n "$hits" ]; then
    printf 'bash-4 or GNU-only construct in a guard:\n%s\n' "$hits"
    return 1
  fi
}
