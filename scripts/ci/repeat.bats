#!/usr/bin/env bats
# BATS tests for scripts/ci/repeat.sh and repeat.yml, the lane that runs a test
# many times on the runner. The go command is a stub answering from the
# environment, so every verdict is reproduced without compiling anything.
# Run by `make ci-scripts-check`.

setup() {
  CI_DIR="$(cd "$BATS_TEST_DIRNAME" && pwd)"
  REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cat >"$BATS_TEST_TMPDIR/bin/go" <<'STUB'
#!/bin/sh
if [ "$1" = version ]; then echo "go version stub"; exit 0; fi
echo "$*" >>"$STUB_LOG"
case "$STUB_MODE" in
fail) echo "--- FAIL: TestX (0.00s)"; echo FAIL; exit 1 ;;
none) echo "ok  	pkg	0.1s [no tests to run]"; exit 0 ;;
*) echo "ok  	pkg	0.1s"; exit 0 ;;
esac
STUB
  chmod +x "$BATS_TEST_TMPDIR/bin/go"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
  export STUB_LOG="$BATS_TEST_TMPDIR/go.log" STUB_MODE=pass
  export REPEAT_OUT="$BATS_TEST_TMPDIR/out" REPEAT_RUN=TestX REPEAT_PACKAGE=./kernel/semver
  unset GITHUB_STEP_SUMMARY
}

@test "repeat: every pass runs, in batches, under -race, GOWORK off, and the verdict is PASS" {
  REPEAT_COUNT=250 REPEAT_BATCH=100 run bash "$CI_DIR/repeat.sh"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^test ' "$STUB_LOG")" -eq 3 ]
  grep -q -- '-count=100 -run ^(TestX)$ ' "$STUB_LOG"
  grep -q -- '-count=50 ' "$STUB_LOG"
  grep -q -- '-race' "$STUB_LOG"
  grep -q '^verdict=PASS$' "$REPEAT_OUT/result.env"
  grep -q '^passes=250$' "$REPEAT_OUT/result.env"
  [ -s "$REPEAT_OUT/raw.txt" ]
}

@test "repeat: a failing pass fails the run and the summary keeps the first trace" {
  STUB_MODE=fail REPEAT_COUNT=2 run bash "$CI_DIR/repeat.sh"
  [ "$status" -ne 0 ]
  grep -q '^verdict=FAIL$' "$REPEAT_OUT/result.env"
  grep -q -- '--- FAIL: TestX' "$REPEAT_OUT/summary.md"
}

@test "repeat: failfast stops at the first failing batch" {
  STUB_MODE=fail REPEAT_COUNT=300 REPEAT_BATCH=100 REPEAT_FAILFAST=true run bash "$CI_DIR/repeat.sh"
  [ "$status" -ne 0 ]
  [ "$(grep -c '^test ' "$STUB_LOG")" -eq 1 ]
}

@test "repeat: a pattern that matches no test is a failure, not a measurement" {
  STUB_MODE=none REPEAT_COUNT=1 run bash "$CI_DIR/repeat.sh"
  [ "$status" -ne 0 ]
  grep -q '^verdict=FAIL$' "$REPEAT_OUT/result.env"
}

@test "repeat: race off drops -race" {
  REPEAT_COUNT=1 REPEAT_RACE=false run bash "$CI_DIR/repeat.sh"
  [ "$status" -eq 0 ]
  run grep -c -- '-race' "$STUB_LOG"
  [ "$output" = 0 ]
}

@test "repeat: the summary is appended to GITHUB_STEP_SUMMARY" {
  GITHUB_STEP_SUMMARY="$BATS_TEST_TMPDIR/step.md" REPEAT_COUNT=1 run bash "$CI_DIR/repeat.sh"
  [ "$status" -eq 0 ]
  grep -q '## repeat: PASS' "$BATS_TEST_TMPDIR/step.md"
}

# An input is never trusted: each one is refused before a command exists.
@test "repeat: refused inputs exit 64 and run nothing" {
  for bad in \
    'REPEAT_RUN=TestX;id' 'REPEAT_RUN=' 'REPEAT_RUN=A||B' 'REPEAT_RUN=$(id)' \
    'REPEAT_COUNT=0' 'REPEAT_COUNT=2001' 'REPEAT_COUNT=1e3' \
    'REPEAT_MODULE=../x' 'REPEAT_MODULE=/etc' 'REPEAT_MODULE=nope' \
    'REPEAT_PACKAGE=../x' 'REPEAT_PACKAGE=kernel' 'REPEAT_PACKAGE=./a b' \
    'REPEAT_RACE=yes' 'REPEAT_GOMAXPROCS=0' 'REPEAT_DEADLINE=0' 'REPEAT_DEADLINE=331'; do
    run env "$bad" bash "$CI_DIR/repeat.sh"
    [ "$status" -eq 64 ] || { echo "accepted: $bad" >&2; return 1; }
  done
  [ ! -e "$STUB_LOG" ]
}

@test "repeat.yml: dispatch only, read-only, SHA-pinned, inputs through env and never in run:" {
  wf="$REPO_ROOT/.github/workflows/repeat.yml"
  grep -q '^  workflow_dispatch:' "$wf"
  run grep -nE '^  (push|pull_request|pull_request_target|schedule|merge_group):' "$wf"
  [ "$status" -ne 0 ]
  grep -q '^  contents: read$' "$wf"
  run grep -nE ': write' "$wf"
  [ "$status" -ne 0 ]
  run grep -nE 'uses: [^@ ]+@' "$wf"
  [ "$status" -eq 0 ]
  run grep -nE 'uses: [^@ ]+@([^ ]{0,39}|[^ ]{41,})( |$)' "$wf"
  [ "$status" -ne 0 ]
  # no ${{ }} expression inside a run: block: only env: and with: take them
  run awk '/^ *run: /{r=1; if ($0 ~ /\$\{\{/) bad=1} r && /\$\{\{/{bad=1} /^ *(env|with|uses|name|shell|if):/{r=0} END{exit !bad}' "$wf"
  [ "$status" -ne 0 ]
  grep -q 'bash scripts/ci/repeat.sh' "$wf"
}
