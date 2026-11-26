#!/usr/bin/env bats
# BATS tests for check-target-fresh.sh and the step of sdk-release.yml that runs
# it (#1497): a release whose target is behind main while a later merge changed
# .github/workflows is stopped with a notice, because GitHub refuses a tag on
# such a commit as a workflow update by an App with no `workflows` permission.

CHECK=""
WF=""

# g — every fixture git command, with hooks disabled at the one precedence that
# wins: `core.hooksPath` is inherited from the developer's own git config, and
# only a command-line `-c` outranks it and an injected GIT_CONFIG_KEY_* (ADR 0088).
g() { git -c core.hooksPath="$NOHOOKS" "$@"; }

setup() {
  NOHOOKS="$BATS_TEST_TMPDIR/nohooks"
  mkdir -p "$NOHOOKS"
  CHECK="$BATS_TEST_DIRNAME/check-target-fresh.sh"
  WF="$BATS_TEST_DIRNAME/../../.github/workflows/sdk-release.yml"
  REPO="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$REPO/.github/workflows"
  cd "$REPO"
  g init -q -b main .
  # Every commit spawns `git maintenance run --auto --detach`, which outlives
  # the command and can race the teardown's `rm -rf`; a throwaway repository
  # needs no upkeep, and the setting precedes the first commit.
  g config maintenance.auto false
  g config user.email t@example.invalid
  g config user.name t
  echo a >.github/workflows/ci.yml
  echo a >file.txt
  g add -A
  g commit -q -m base
  TARGET="$(g rev-parse HEAD)"
}

# step <name> — the lines of the step whose `- name:` is <name>.
step() {
  awk -v want="      - name: $1" '
    $0 == want                    { grab = 1; print; next }
    grab && /^      - name:/      { exit }
    grab                          { print }
  ' "$WF"
}

@test "a target behind main with different workflows stops the release with a notice" {
  echo b >.github/workflows/ci.yml
  g commit -q -am "touch a workflow"
  run bash "$CHECK" "$TARGET" main
  [ "$status" -eq 10 ]
  [[ "$output" == *"behind main"* ]]
  [[ "$output" == *"Nothing is cut"* ]]
}

@test "a new workflow file on main counts as a difference" {
  echo x >.github/workflows/new.yml
  g add -A
  g commit -q -m "add a workflow"
  run bash "$CHECK" "$TARGET" main
  [ "$status" -eq 10 ]
}

@test "a target behind main with identical workflows lets the cut go on" {
  echo b >file.txt
  g commit -q -am "later merge outside the workflows"
  run bash "$CHECK" "$TARGET" main
  [ "$status" -eq 0 ]
  [[ "$output" == *"behind main"* ]]
  [[ "$output" == *"the cut can go on"* ]]
}

@test "a target that is main's head lets the cut go on" {
  run bash "$CHECK" "$TARGET" main
  [ "$status" -eq 0 ]
  [[ "$output" == *"is the head of main"* ]]
}

@test "an unresolvable ref fails closed, never as fresh" {
  run bash "$CHECK" "$TARGET" no-such-ref
  [ "$status" -eq 2 ]
  run bash "$CHECK" 0000000000000000000000000000000000000000 main
  [ "$status" -eq 2 ]
}

@test "bad usage is refused" {
  run bash "$CHECK"
  [ "$status" -eq 64 ]
}

@test "the workflow runs the check before computing, and a stale target computes nothing" {
  body="$(step "Stop when the target is behind main and the workflows differ")"
  [ -n "$body" ]
  [[ "$body" == *'git fetch --quiet origin main'* ]]
  [[ "$body" == *'git show "${GITHUB_SHA}:scripts/release/check-target-fresh.sh"'* ]]
  [[ "$body" == *'bash "$check" "$TARGET_SHA" origin/main'* ]]
  # Never the target's own tree, which may predate the script (127).
  [[ "$body" != *'bash scripts/release/check-target-fresh.sh'* ]]
  [[ "$body" == *'stale=true'* ]]
  # No exit 1 for the stale case: it is a notice.
  [[ "$body" == *'::notice::'* ]]
  fresh_line="$(grep -n 'name: Stop when the target is behind main' "$WF" | cut -d: -f1)"
  compute_line="$(grep -n 'name: Compute the release token' "$WF" | cut -d: -f1)"
  [ "$fresh_line" -lt "$compute_line" ]
  body="$(step "Compute the release token")"
  [[ "$body" == *'STALE: ${{ steps.fresh.outputs.stale }}'* ]]
  [[ "$body" == *'"${STALE:-}" = "true"'* ]]
}

@test "the script is read from the workflow's revision when the target predates it" {
  # The target has no scripts/release at all; the workflow's revision does.
  echo b >file.txt
  mkdir -p scripts/release
  cp "$CHECK" scripts/release/check-target-fresh.sh
  echo b >.github/workflows/ci.yml
  g add -A
  g commit -q -m "workflow revision"
  WFREV="$(g rev-parse HEAD)"
  g checkout -q "$TARGET"
  [ ! -e scripts/release/check-target-fresh.sh ]
  loaded="$BATS_TEST_TMPDIR/loaded.sh"
  g show "${WFREV}:scripts/release/check-target-fresh.sh" >"$loaded"
  run bash "$loaded" "$TARGET" "$WFREV"
  [ "$status" -eq 10 ]
  [[ "$output" == *"Nothing is cut"* ]]
}

@test "a revision that does not hold the script fails closed" {
  run g show "${TARGET}:scripts/release/check-target-fresh.sh"
  [ "$status" -ne 0 ]
}
