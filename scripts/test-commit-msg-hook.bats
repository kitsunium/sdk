#!/usr/bin/env bats
# BATS tests for .githooks/commit-msg — the gate that refuses AI attribution in
# a commit message.
#
# This hook shipped with `printf '%s' "$MSG" | grep -qiE "$pattern"` as the
# condition of an `if`. That construct has a failure mode which is not merely
# broken but INVERTED: `grep -q` exits on its first match, the writer takes
# SIGPIPE, `set -o pipefail` makes the pipeline 141, and 141 is false — so the
# `if` does not fire and the hook ALLOWS the message it exists to refuse. `set
# -e` does not rescue it either: a failing command in an `if` condition is
# exempt.
#
# Measured before the fix, on this repository's hook, `Co-Authored-By: Claude`
# placed near the top and a body of filler after it, 40 runs per cell:
#
#   body size   pattern early   pattern late
#   1 000       0/40 pass       0/40
#   64 000      0/40            0/40
#   70 000      1/40            0/40
#   96 000      27/40           0/40
#   200 000     40/40           0/40
#
# "pass" means the hook exited 0 and the attribution went through. Below the
# pipe buffer the gate works; above it, it stops working, and past ~200 KB it
# stops working every single time. The right-hand column is the control: with
# the pattern at the END, grep must read the whole input, there is no early
# exit, no SIGPIPE, and the gate holds at every size. A gate whose correctness
# depends on where in the input the thing it looks for happens to sit is not a
# gate.
#
# The sizes below are chosen from that table: SMALL is the regime the hook was
# presumably tested in, LARGE is the deterministic-bypass regime.

setup() {
  HOOK="$BATS_TEST_DIRNAME/../.githooks/commit-msg"
  MSG="$BATS_TEST_TMPDIR/msg.txt"
}

# filler <bytes> — printable padding, no newline at the end.
filler() { head -c "$1" /dev/zero | tr '\0' 'x'; }

SMALL=1000
LARGE=400000

@test "a clean message is accepted" {
  printf 'fix(codec): tighten the bounds check\n\nNo attribution here.\n' >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 0 ]
}

@test "a clean message is accepted at 400 KB" {
  { printf 'fix(codec): tighten the bounds check\n\n'; filler 400000; printf '\n'; } >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 0 ]
}

@test "Co-Authored-By: Claude is refused in a short message" {
  printf 'fix: x\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n' >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 1 ]
  [[ "$output" == *"COMMIT BLOCKED"* ]]
}

# The regression. With the pattern early and the body past the pipe buffer, the
# pre-fix hook exited 0 on 40 runs out of 40 and the attribution was committed.
@test "Co-Authored-By: Claude is refused when the body is past the pipe buffer" {
  { printf 'fix: x\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n\n'
    filler "$LARGE"; printf '\n'; } >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 1 ]
  [[ "$output" == *"COMMIT BLOCKED"* ]]
}

# The same fact from the other side: the pre-fix hook DID catch this one, at
# every size, because grep had to read to the end and never closed the pipe
# early. Keeping it pins the asymmetry that the bug consisted of.
@test "a trailing attribution is refused at the same size" {
  { printf 'fix: x\n\n'; filler "$LARGE"
    printf '\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n'; } >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 1 ]
  [[ "$output" == *"COMMIT BLOCKED"* ]]
}

# The pattern list is matched case-insensitively against a lowercased message,
# so the hook must not be escapable by shouting.
@test "the match is case-insensitive past the pipe buffer" {
  { printf 'fix: x\n\nCO-AUTHORED-BY: CLAUDE <NOREPLY@ANTHROPIC.COM>\n\n'
    filler "$LARGE"; printf '\n'; } >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 1 ]
}

# "generated with claude" is a separate entry in the list, and it is the one
# that appears in the tool-generated PR/commit boilerplate this policy exists
# to keep out. It must survive the same size.
@test "Generated with Claude Code is refused past the pipe buffer" {
  { printf 'fix: x\n\n🤖 Generated with Claude Code\n\n'
    filler "$LARGE"; printf '\n'; } >"$MSG"
  run "$HOOK" "$MSG"
  [ "$status" -eq 1 ]
}

# The hook takes the message PATH, not the message. A missing argument must be
# a loud failure rather than an empty match that accepts everything.
@test "a missing message path is refused, not treated as an empty message" {
  run "$HOOK"
  [ "$status" -ne 0 ]
}
