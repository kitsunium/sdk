#!/usr/bin/env bats
# BATS tests for what a run tests (ADR 0250): scripts/ci/scope.sh — the plan
# in throwaway repositories (the events, the paths a change touches, the
# fallbacks, a computation that fails), the packages a plan reaches over a
# stub go command, the lanes' decisions, the Bazel scope over a stub
# target-determinator and a stub bazel, the summary's verdict, the helpers
# the workflows call — scripts/ci/test-binaries.sh restricted to the packages
# in scope, scripts/ci/bazel-remote-cache.sh on every event, and the shape of
# bazel-ci.yml and e2e-cross.yml: a lane skips only on the plan's explicit
# `skip`, every scoped step falls back to the whole suite, the tools pinned,
# the write credential handed out on the events that may write alone. Run by
# `make ci-scripts-check`.

setup() {
  CI_DIR="$(cd "$BATS_TEST_DIRNAME" && pwd)"
  REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
  BAZEL_CI="$REPO_ROOT/.github/workflows/bazel-ci.yml"
  E2E="$REPO_ROOT/.github/workflows/e2e-cross.yml"
  WORK="$BATS_TEST_TMPDIR/work"
  mkdir -p "$WORK"
  export SCOPE_REPORT="$BATS_TEST_TMPDIR/report"
  : >"$SCOPE_REPORT"
}

# git, with no hook of the host's: a fixture never runs one.
g() { git -c core.hooksPath=/dev/null "$@"; }

# put <path> [content] — a file of the fixture, its directories made.
put() {
  mkdir -p "$(dirname "$1")"
  printf '%s\n' "${2:-x}" >"$1"
}

# fixture — a repository holding this directory's scripts and two modules: the
# root (example.com/sdk, the framework) and internal (example.com/sdk/internal),
# a kernel package with a test, a leaf codec, a framework package embedding a
# catalogue, docs, a design file and a workflow. BASE is its one commit.
fixture() {
  cd "$WORK" || return 1
  g init -q .
  g config user.email t@example.com
  g config user.name t
  mkdir -p scripts/ci tools
  cp "$CI_DIR/scope.sh" "$CI_DIR/go-modules.sh" "$CI_DIR/test-binaries.sh" scripts/ci/
  put tools/alloc-lane-targets.txt '//internal/kernel/...'
  printf 'module example.com/sdk\n\ngo 1.27\n' >go.mod
  mkdir -p internal
  printf 'module example.com/sdk/internal\n\ngo 1.27\n' >internal/go.mod
  put internal/kernel/clock/clock.go 'package clock'
  put internal/kernel/clock/clock_test.go 'package clock'
  put internal/kernel/clock/CLAUDE.md '# clock'
  put internal/kernel/clock/BUILD.bazel '# build'
  put internal/kernel/clock/testdata/golden.txt 'golden'
  put internal/service/data/codec/tlv/tlv.go 'package tlv'
  put internal/CLAUDE.md '# internal'
  put framework/internal/app/words.go $'package app\n\n//go:embed locales/en.json\nvar en []byte'
  put framework/internal/app/locales/en.json '{}'
  put framework/app/app.go 'package app'
  put framework/app/testdata/probe/main.go 'package main'
  put docs/adr/0001-x.md '# x'
  put docs/api/sdk.json '{}'
  put design/net.yaml 'net: {}'
  put design/app/lock.yaml 'lock: {}'
  put .github/workflows/bazel-ci.yml 'name: x'
  put .github/workflows/docs-deploy.yml 'name: y'
  put README.md '# sdk'
  g add -A
  g commit -qm base
  BASE="$(g rev-parse HEAD)"
}

# change <path>... — a second commit touching each path (a new file, or a line
# appended), leaving HEAD_SHA.
change() {
  local p
  for p in "$@"; do
    mkdir -p "$(dirname "$p")"
    echo "# changed" >>"$p"
    g add -- "$p"
  done
  g commit -qm change
  HEAD_SHA="$(g rev-parse HEAD)"
}

# plan_pr [SELF...] — the plan of a pull request from BASE to HEAD_SHA.
plan_pr() {
  run bash scripts/ci/scope.sh plan pull_request refs/pull/1/merge "$BASE" "$HEAD_SHA" "$@"
}

# field <key> — one line of the last plan's answer.
field() { sed -n "s/^$1=//p" <<<"$output"; }

# ── plan: the events ────────────────────────────────────────────────────────

@test "plan: a pull request is scoped" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  plan_pr
  [ "$status" -eq 0 ]
  [ "$(field mode)" = scoped ]
}

@test "plan: the queue, main, a queue branch, a tag, the nightly and a manual run are whole" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  for ev in "merge_group refs/heads/gh-readonly-queue/main/pr-1-abc" "push refs/heads/main" \
    "push refs/heads/gh-readonly-queue/main/pr-1-abc" "push refs/tags/v1.0.0" "schedule refs/heads/main" \
    "workflow_dispatch refs/heads/feature" "release refs/tags/v1"; do
    read -r event ref <<<"$ev"
    run bash scripts/ci/scope.sh plan "$event" "$ref" "$BASE" "$HEAD_SHA"
    [ "$status" -eq 0 ]
    [ "$(field mode)" = full ] || { echo "$ev: $(field mode)"; return 1; }
    [ -z "$(field touched)" ]
  done
}

@test "plan: a push to another branch is scoped against its merge base with main" {
  fixture
  g update-ref refs/remotes/origin/main "$BASE"
  change internal/service/data/codec/tlv/tlv.go
  run bash scripts/ci/scope.sh plan push refs/heads/feature "" "$HEAD_SHA"
  [ "$status" -eq 0 ]
  [ "$(field mode)" = scoped ]
  [ "$(field touched)" = example.com/sdk/internal/service/data/codec/tlv ]
}

# ── plan: the paths ─────────────────────────────────────────────────────────

@test "paths: a package's code reaches its dependents, a Go file every test that reads the sources" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  plan_pr
  [ "$(field mode)" = scoped ]
  [ "$(field touched)" = example.com/sdk/internal/service/data/codec/tlv ]
  # The error-code audits and genindex's census read every Go source.
  [[ " $(field tests) " == *" example.com/sdk/internal/kernel/errs "* ]]
  [[ " $(field tests) " == *" example.com/sdk/tools/genindex "* ]]
  [[ " $(field tests) " != *"/tlv "* ]]
}

@test "paths: a test file, a testdata file and prose reach the package's own tests alone" {
  fixture
  change internal/kernel/clock/clock_test.go internal/kernel/clock/testdata/golden.txt internal/kernel/clock/CLAUDE.md
  plan_pr
  [ "$(field mode)" = scoped ]
  [ -z "$(field touched)" ]
  [[ " $(field tests) " == *" example.com/sdk/internal/kernel/clock "* ]]
}

@test "paths: a file beside a package that embeds files reaches its dependents" {
  fixture
  change framework/internal/app/locales/en.json
  plan_pr
  [ "$(field mode)" = scoped ]
  [ "$(field touched)" = example.com/sdk/framework/internal/app ]
  # framework/app's no-trace guard reads that catalogue.
  [[ " $(field tests) " == *" example.com/sdk/framework/app "* ]]
}

@test "paths: a program under testdata/ reaches the package that builds it, not its dependents" {
  fixture
  change framework/app/testdata/probe/main.go
  plan_pr
  [ "$(field mode)" = scoped ]
  [ -z "$(field touched)" ]
  [[ " $(field tests) " == *" example.com/sdk/framework/app "* ]]
}

@test "paths: BUILD files, prose, ADRs, the design and another workflow touch no Go package" {
  fixture
  change internal/kernel/clock/BUILD.bazel internal/CLAUDE.md docs/adr/0001-x.md design/app/lock.yaml \
    .github/workflows/docs-deploy.yml README.md
  plan_pr
  [ "$(field mode)" = scoped ]
  [ -z "$(field touched)" ]
  [ -z "$(field tests)" ]
}

@test "paths: a file a test reads outside its package reaches that test" {
  fixture
  change design/net.yaml docs/api/sdk.json
  plan_pr
  [ "$(field mode)" = scoped ]
  [ -z "$(field touched)" ]
  [[ " $(field tests) " == *" example.com/sdk/internal/service/net/server "* ]]
  [[ " $(field tests) " == *" example.com/sdk/tools/genindex "* ]]
}

@test "paths: a deleted package and a file moved out of one still touch it" {
  fixture
  g rm -q -r internal/service/data/codec/tlv
  g mv internal/kernel/clock/clock.go internal/kernel/clock2.go
  g commit -qm move
  HEAD_SHA="$(g rev-parse HEAD)"
  plan_pr
  [ "$(field mode)" = scoped ]
  [[ " $(field touched) " == *" example.com/sdk/internal/service/data/codec/tlv "* ]]
  [[ " $(field touched) " == *" example.com/sdk/internal/kernel/clock "* ]]
}

# ── plan: the fallbacks ─────────────────────────────────────────────────────

@test "fallback: the build's definition runs everything" {
  for p in MODULE.bazel MODULE.bazel.lock .bazelrc .bazelversion .bazelignore BUILD.bazel WORKSPACE \
    go.mod internal/go.mod third-party/x/go.sum go.work go.work.sum tools/alloc-lane-targets.txt \
    .gitattributes scripts/ci/scope.sh; do
    rm -rf "$WORK"
    mkdir -p "$WORK"
    fixture
    change "$p"
    plan_pr
    [ "$status" -eq 0 ]
    [ "$(field mode)" = fallback ] || { echo "$p: $(field mode)"; return 1; }
    [[ "$(field reason)" == "$p changed"* ]]
    [ -z "$(field touched)" ]
  done
}

@test "fallback: the calling workflow's own files run everything, another workflow does not" {
  fixture
  change .github/workflows/bazel-ci.yml
  plan_pr '^\.github/workflows/bazel-ci\.yml$' '^scripts/ci/'
  [ "$(field mode)" = fallback ]
  [[ "$(field reason)" == *"the workflow's own files"* ]]
  plan_pr '^\.github/workflows/e2e-cross\.yml$'
  [ "$(field mode)" = scoped ]
}

@test "fallback: a path no rule accounts for runs everything" {
  fixture
  change newtree/data.bin
  plan_pr
  [ "$(field mode)" = fallback ]
  [[ "$(field reason)" == "newtree/data.bin: outside every Go package, and no rule says what reads it" ]]
}

@test "fallback: a plan past the size a step's environment carries runs everything" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  SCOPE_TOUCHED_MAX=10 plan_pr
  [ "$(field mode)" = fallback ]
  [[ "$(field reason)" == *"too many packages"* ]]
}

# ── plan: a computation that fails ──────────────────────────────────────────

@test "failure: a base this clone does not hold falls back, never answers scoped" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  run bash scripts/ci/scope.sh plan pull_request refs/pull/1/merge 0123456789abcdef0123456789abcdef01234567 "$HEAD_SHA"
  [ "$status" -eq 0 ]
  [ "$(field mode)" = fallback ]
  [[ "$(field reason)" == *"is not in this clone"* ]]
}

@test "failure: no base, or a branch with no main to compare with, falls back" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  run bash scripts/ci/scope.sh plan pull_request refs/pull/1/merge "" "$HEAD_SHA"
  [ "$(field mode)" = fallback ]
  run bash scripts/ci/scope.sh plan push refs/heads/feature "" "$HEAD_SHA"
  [ "$(field mode)" = fallback ]
  [[ "$(field reason)" == "no base commit to compare with" ]]
}

@test "failure: a git that cannot answer falls back" {
  fixture
  change internal/service/data/codec/tlv/tlv.go
  GIT_DIR="$WORK/no-such-git-dir" run bash scripts/ci/scope.sh plan pull_request x "$BASE" "$HEAD_SHA"
  [ "$status" -eq 0 ]
  [ "$(field mode)" = fallback ]
}

@test "failure: bad usage is refused" {
  run bash "$CI_DIR/scope.sh"
  [ "$status" -eq 64 ]
  run bash "$CI_DIR/scope.sh" plan pull_request
  [ "$status" -eq 64 ]
  run bash "$CI_DIR/scope.sh" nonsense
  [ "$status" -eq 64 ]
}

# ── affected, over a stub go ────────────────────────────────────────────────

# stub_go — a go command answering `go list` from files: <key>.list for the
# packages of a module (key: its directory, / spelled _, the root `root`),
# <key>.<program>.deps for a testdata program's, failing where <key>.fail is.
stub_go() {
  mkdir -p "$WORK/stub" "$WORK/answers"
  cat >"$WORK/stub/go" <<'SH'
#!/usr/bin/env bash
rel="${PWD#"$STUB_ROOT"}"
rel="${rel#/}"
[ -n "$rel" ] || rel=root
key="${rel//\//_}"
if [ "$1" = test ]; then
  printf '%s %s\n' "$rel" "$*" >>"$STUB_ANSWERS/tests.log"
  [ -f "$STUB_ANSWERS/$key.testfail" ] && exit 1
  exit 0
fi
[ -f "$STUB_ANSWERS/$key.fail" ] && exit 1
case " $* " in
  *" -test "*) cat "$STUB_ANSWERS/$key.list" 2>/dev/null ;;
  *" -deps "*) last="${*: -1}"; cat "$STUB_ANSWERS/$key.$(basename "$last").deps" 2>/dev/null ;;
esac
exit 0
SH
  chmod +x "$WORK/stub/go"
  export STUB_ROOT="$WORK" STUB_ANSWERS="$WORK/answers" PATH="$WORK/stub:$PATH"
  T=$'\t'
  # The internal module: clock (a kernel package), tlv importing clock, a
  # codec aggregate whose TEST binary links tlv, a package no file of builds
  # here, and one reaching nothing.
  {
    echo "example.com/sdk/internal/kernel/clock${T}${T}"
    echo "example.com/sdk/internal/kernel/clock [example.com/sdk/internal/kernel/clock.test]${T}${T}"
    echo "example.com/sdk/internal/kernel/clock.test${T}${T}example.com/sdk/internal/kernel/clock [example.com/sdk/internal/kernel/clock.test]"
    echo "example.com/sdk/internal/service/data/codec/tlv${T}${T}example.com/sdk/internal/kernel/clock"
    echo "example.com/sdk/internal/service/data/codec${T}${T}"
    echo "example.com/sdk/internal/service/data/codec_test [example.com/sdk/internal/service/data/codec.test]${T}${T}example.com/sdk/internal/service/data/codec/tlv"
    echo "example.com/sdk/internal/service/data/codec.test${T}${T}example.com/sdk/internal/service/data/codec_test [example.com/sdk/internal/service/data/codec.test],example.com/sdk/internal/service/data/codec/tlv"
    echo "example.com/sdk/internal/kernel/winonly${T}build constraints exclude all Go files in /x${T}example.com/sdk/internal/kernel/clock"
    echo "example.com/sdk/internal/kernel/other${T}${T}"
  } >"$WORK/answers/internal.list"
  # The root module: framework/app, whose testdata program imports tlv.
  echo "example.com/sdk/framework/app${T}${T}" >"$WORK/answers/root.list"
  printf '%s\n' example.com/sdk/framework/app/testdata/probe example.com/sdk/internal/service/data/codec/tlv \
    >"$WORK/answers/root.probe.deps"
}

@test "affected: a package, what imports it, what a test binary links, and what a testdata program builds" {
  fixture
  stub_go
  SCOPE_TOUCHED=example.com/sdk/internal/service/data/codec/tlv SCOPE_TESTS='' run bash scripts/ci/scope.sh affected
  [ "$status" -eq 0 ]
  [ "$output" = ".${T}example.com/sdk/framework/app${T}framework/app
internal${T}example.com/sdk/internal/service/data/codec/tlv${T}internal/service/data/codec/tlv
internal${T}example.com/sdk/internal/service/data/codec${T}internal/service/data/codec" ]
}

@test "affected: a package's own tests reach no dependent, and a package excluded here is left out" {
  fixture
  stub_go
  SCOPE_TOUCHED='' SCOPE_TESTS=example.com/sdk/internal/kernel/clock run bash scripts/ci/scope.sh affected
  [ "$status" -eq 0 ]
  [ "$output" = "internal${T}example.com/sdk/internal/kernel/clock${T}internal/kernel/clock" ]
  SCOPE_TOUCHED=example.com/sdk/internal/kernel/clock SCOPE_TESTS='' run bash scripts/ci/scope.sh affected
  [[ "$output" == *"/kernel/clock${T}"* ]]
  [[ "$output" == *"/codec/tlv${T}"* ]]
  [[ "$output" != *"winonly"* ]]
  [[ "$output" != *"other"* ]]
}

@test "affected: a go list that fails fails, never answers nothing" {
  fixture
  stub_go
  : >"$WORK/answers/internal.fail"
  SCOPE_TOUCHED=example.com/sdk/internal/kernel/clock run bash scripts/ci/scope.sh affected
  [ "$status" -eq 1 ]
  [[ "$output" == *"go list failed in internal"* ]]
}

# ── lane ────────────────────────────────────────────────────────────────────

@test "lane: a whole plan runs, a plan touching nothing skips, a reached lane runs" {
  fixture
  stub_go
  SCOPE_MODE=full run bash scripts/ci/scope.sh lane x
  [ "$output" = "x=run" ] || [[ "$output" == "x=run"$'\n'* ]]
  SCOPE_MODE=fallback run bash scripts/ci/scope.sh lane x
  [[ "$output" == "x=run"* ]]
  SCOPE_MODE=scoped SCOPE_TOUCHED='' SCOPE_TESTS='' run bash scripts/ci/scope.sh lane x
  [[ "$output" == "x=skip"* ]]
  SCOPE_MODE=scoped SCOPE_TOUCHED=example.com/sdk/internal/kernel/clock run bash scripts/ci/scope.sh lane x GOOS=linux,GOARCH=386
  [[ "$output" == "x=run"* ]]
}

@test "lane: a lane whose packages are not reached skips; one reached under any config runs" {
  fixture
  stub_go
  SCOPE_MODE=scoped SCOPE_TOUCHED=example.com/sdk/internal/kernel/clock \
    run bash scripts/ci/scope.sh lane simd host -- example.com/sdk/internal/kernel/swar
  [[ "$output" == "simd=skip"* ]]
  SCOPE_MODE=scoped SCOPE_TOUCHED=example.com/sdk/internal/kernel/clock \
    run bash scripts/ci/scope.sh lane bsd GOOS=freebsd,GOARCH=amd64 GOOS=netbsd,GOARCH=amd64 -- example.com/sdk/internal/service/data/codec/tlv
  [[ "$output" == "bsd=run"* ]]
}

@test "lane: a computation that fails runs the lane, with a warning" {
  fixture
  stub_go
  : >"$WORK/answers/root.fail"
  SCOPE_MODE=scoped SCOPE_TOUCHED=example.com/sdk/internal/kernel/clock run bash scripts/ci/scope.sh lane x host
  grep -qx 'x=run' <<<"$output"
  [[ "$output" == *"::warning::"* ]]
}

# ── bazel, over a stub target-determinator and a stub bazel ─────────────────

# stub_bazel — a target-determinator printing td.out (failing where td.fail
# is) and recording its arguments, and a bazel answering a query from
# query.tests or query.alloc (failing where query.fail is).
stub_bazel() {
  mkdir -p "$WORK/stub" "$WORK/answers"
  cat >"$WORK/stub/td" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$STUB_ANSWERS/td.args"
pwd >"$STUB_ANSWERS/td.pwd"
git rev-parse HEAD >"$STUB_ANSWERS/td.head"
# What a killed target-determinator leaves behind: the base checked out.
if [ -f "$STUB_ANSWERS/td.disturb" ]; then
  git -C "$STUB_MAIN" -c core.hooksPath=/dev/null checkout -q --detach "$(cat "$STUB_ANSWERS/td.disturb")"
fi
[ -f "$STUB_ANSWERS/td.fail" ] && { echo "td: boom" >&2; exit 1; }
cat "$STUB_ANSWERS/td.out"
SH
  cat >"$WORK/stub/bazel" <<'SH'
#!/usr/bin/env bash
[ -f "$STUB_ANSWERS/query.fail" ] && exit 7
q=""
for a in "$@"; do case "$a" in --query_file=*) q="${a#--query_file=}" ;; esac; done
cat "$q" >>"$STUB_ANSWERS/queries.log"
if grep -q intersect "$q"; then cat "$STUB_ANSWERS/query.alloc"; else cat "$STUB_ANSWERS/query.tests"; fi
SH
  chmod +x "$WORK/stub/td" "$WORK/stub/bazel"
  export STUB_ANSWERS="$WORK/answers" BAZEL="$WORK/stub/bazel" SCOPE_TD="$WORK/stub/td" STUB_MAIN="$WORK"
  printf '%s\n' 'Some log line' //internal/kernel/clock:clock //internal/kernel/clock:clock_test >"$WORK/answers/td.out"
  echo //internal/kernel/clock:clock_test >"$WORK/answers/query.tests"
  echo //internal/kernel/clock:clock_test >"$WORK/answers/query.alloc"
}

@test "bazel: a scoped plan lists the affected targets, their tests and the allocation tests among them" {
  fixture
  stub_bazel
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE=abc123 run bash scripts/ci/scope.sh bazel "$WORK/out"
  [ "$status" -eq 0 ]
  [[ "$output" == *"mode=scoped"* ]]
  [[ "$output" == *"targets=2"* ]]
  [[ "$output" == *"tests=1"* ]]
  [[ "$output" == *"alloc=1"* ]]
  [ "$(cat "$WORK/out/targets.txt")" = $'//internal/kernel/clock:clock\n//internal/kernel/clock:clock_test' ]
  # Against the base, manual targets left out, with the CI configuration.
  grep -qx abc123 "$WORK/answers/td.args"
  grep -qx -- '-targets=//... except attr(tags, "manual", //...)' "$WORK/answers/td.args"
  grep -qx -- '-bazel-opts=--config=ci' "$WORK/answers/td.args"
  grep -q 'tests(set("//internal/kernel/clock:clock" "//internal/kernel/clock:clock_test"))' "$WORK/answers/queries.log"
  grep -q 'set(//internal/kernel/...) intersect set(' "$WORK/answers/queries.log"
}

# target-determinator checks the base out where it runs: never in the
# checkout under test, whose commit and state it must leave as they were.
@test "bazel: target-determinator runs in a clone of the commit under test, removed after" {
  fixture
  change internal/kernel/clock/clock.go
  stub_bazel
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE="$BASE" run bash scripts/ci/scope.sh bazel "$WORK/out"
  [ "$status" -eq 0 ]
  [ "$(cat "$WORK/answers/td.pwd")" = "$WORK/out/clone" ]
  [ "$(cat "$WORK/answers/td.head")" = "$HEAD_SHA" ]
  [ ! -e "$WORK/out/clone" ]
  [ "$(g rev-parse HEAD)" = "$HEAD_SHA" ]
}

@test "bazel: a checkout under test that changed meanwhile fails, never falls back" {
  fixture
  change internal/kernel/clock/clock.go
  stub_bazel
  # The stub moves the checkout under test, as an in-place run that died would.
  echo "$BASE" >"$WORK/answers/td.disturb"
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE="$BASE" run bash scripts/ci/scope.sh bazel "$WORK/out"
  [ "$status" -eq 2 ]
  [[ "$output" == *"the checkout under test changed"* ]]
  [[ "$output" != *"mode="* ]]
}

@test "bazel: no changed target is a scoped answer with nothing to build" {
  fixture
  stub_bazel
  : >"$WORK/answers/td.out"
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE=abc123 run bash scripts/ci/scope.sh bazel "$WORK/out"
  [[ "$output" == *"mode=scoped"* ]]
  [[ "$output" == *"targets=0"* ]]
  [ ! -s "$WORK/out/targets.txt" ]
}

@test "bazel: a plan that failed, a whole plan or a fallback never call target-determinator" {
  fixture
  stub_bazel
  for env in "SCOPE_RESULT=failure SCOPE_MODE=scoped" "SCOPE_RESULT= SCOPE_MODE=" "SCOPE_RESULT=success SCOPE_MODE=full" \
    "SCOPE_RESULT=success SCOPE_MODE=fallback" "SCOPE_RESULT=success SCOPE_MODE=garbage"; do
    rm -f "$WORK/answers/td.args"
    run env $env SCOPE_BASE=abc123 bash scripts/ci/scope.sh bazel "$WORK/out"
    [ "$status" -eq 0 ]
    [[ "$output" != *"mode=scoped"* ]] || { echo "$env: $output"; return 1; }
    [ ! -f "$WORK/answers/td.args" ]
  done
}

@test "bazel: target-determinator or a query that fails falls back to everything" {
  fixture
  stub_bazel
  : >"$WORK/answers/td.fail"
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE=abc123 run bash scripts/ci/scope.sh bazel "$WORK/out"
  [[ "$output" == *"mode=fallback"* ]]
  [[ "$output" == *"target-determinator failed"* ]]
  rm "$WORK/answers/td.fail"
  : >"$WORK/answers/query.fail"
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE=abc123 run bash scripts/ci/scope.sh bazel "$WORK/out"
  [[ "$output" == *"mode=fallback"* ]]
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE='' run bash scripts/ci/scope.sh bazel "$WORK/out"
  [[ "$output" == *"mode=fallback"* ]]
}

@test "bazel: an unpinned target-determinator is never fetched" {
  fixture
  stub_bazel
  unset SCOPE_TD
  TD_VERSION='' TD_SHA256='' SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_BASE=abc123 \
    run bash scripts/ci/scope.sh bazel "$WORK/out"
  [[ "$output" == *"mode=fallback"* ]]
  [[ "$output" == *"could not be installed"* ]]
}

# ── summary ─────────────────────────────────────────────────────────────────

@test "summary: a lane skipped by scope is said so, with what was judged out of its reach" {
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_REASON=r SCOPE_TOUCHED=example.com/a SCOPE_TESTS='' \
    SCOPE_LANES=$'bazel|run|success|n\ntest-386|skip|skipped|\ncross-build|full|success|' \
    run bash "$CI_DIR/scope.sh" summary
  [ "$status" -eq 0 ]
  [[ "$output" == *'| test-386 | skipped by scope: none of the touched packages reaches it | skipped |'* ]]
  [[ "$output" == *'| bazel | ran (scoped) | success | n |'* ]]
  [[ "$output" == *'| cross-build | ran in full (never scoped) | success |'* ]]
  [[ "$output" == *'`example.com/a`'* ]]
}

@test "summary: a lane in scope that was skipped fails the summary" {
  SCOPE_RESULT=success SCOPE_MODE=scoped SCOPE_TOUCHED=example.com/a SCOPE_LANES='test-386|run|skipped|' \
    run bash "$CI_DIR/scope.sh" summary
  [ "$status" -eq 1 ]
  [[ "$output" == *"skipped although it had to run"* ]]
}

@test "summary: a plan that failed, a full plan or a fallback skip nothing" {
  for env in "SCOPE_RESULT=failure SCOPE_MODE=" "SCOPE_RESULT=success SCOPE_MODE=full" \
    "SCOPE_RESULT=success SCOPE_MODE=fallback" "SCOPE_RESULT=cancelled SCOPE_MODE=scoped"; do
    run env $env SCOPE_LANES='test-386|skip|skipped|' bash "$CI_DIR/scope.sh" summary
    [ "$status" -eq 1 ] || { echo "$env: $output"; return 1; }
    run env $env SCOPE_LANES='test-386|skip|success|' bash "$CI_DIR/scope.sh" summary
    [ "$status" -eq 0 ]
  done
  SCOPE_RESULT=failure run bash "$CI_DIR/scope.sh" summary
  [[ "$output" == *"fallback to full — the scope job ended 'failure'"* ]]
}

# ── the helpers the workflows call ──────────────────────────────────────────

@test "helpers: keep, pkgs and go-test read an affected answer" {
  fixture
  stub_go
  printf 'internal\texample.com/sdk/internal/kernel/clock\tinternal/kernel/clock\n.\texample.com/sdk/framework/app\tframework/app\n' >"$WORK/aff"
  run bash scripts/ci/scope.sh keep "$WORK/aff" internal/kernel ./clock ./other
  [ "$output" = "./clock" ]
  run bash scripts/ci/scope.sh keep "$WORK/aff" framework/app .
  [ "$output" = "." ]
  run bash scripts/ci/scope.sh pkgs "$WORK/aff" . example.com/sdk/framework/
  [ "$output" = example.com/sdk/framework/app ]
  run bash scripts/ci/scope.sh go-test "$WORK/aff" example.com/sdk/framework/ -race -count=1
  [ "$status" -eq 0 ]
  [ "$(cat "$WORK/answers/tests.log")" = "root test -race -count=1 example.com/sdk/framework/app" ]
  : >"$WORK/answers/internal.testfail"
  run bash scripts/ci/scope.sh go-test "$WORK/aff" ""
  [ "$status" -eq 1 ]
  [[ "$output" == *"::error::internal has failing tests"* ]]
  run bash scripts/ci/scope.sh go-test "$WORK/aff" example.com/none/
  [ "$status" -eq 0 ]
  [[ "$output" == *"no package under example.com/none/ in scope"* ]]
}

@test "helpers: test-binaries.sh compiles the packages in scope alone, and an empty scope is an empty manifest" {
  cd "$WORK" || return 1
  mkdir -p repo/scripts/ci repo/internal/a repo/internal/b stub
  cp "$CI_DIR/test-binaries.sh" repo/scripts/ci/
  cat >stub/go <<SH
#!/bin/sh
case "\$1" in
  list) printf '%s\n' "$WORK/repo/internal/a" "$WORK/repo/internal/b" ;;
  env) echo stubos ;;
  test) while [ "\$#" -gt 0 ]; do [ "\$1" = -o ] && : >"\$2"; shift; done ;;
esac
SH
  chmod +x stub/go
  echo internal/b >only.txt
  run env PATH="$WORK/stub:$PATH" TEST_BINARIES_ONLY="$WORK/only.txt" bash repo/scripts/ci/test-binaries.sh "$WORK/bins" internal
  [ "$status" -eq 0 ]
  [ "$(cat "$WORK/bins/manifest")" = "internal__b.test internal/b" ]
  echo framework/x >only.txt
  run env PATH="$WORK/stub:$PATH" TEST_BINARIES_ONLY="$WORK/only.txt" bash repo/scripts/ci/test-binaries.sh "$WORK/bins2" internal
  [ "$status" -eq 0 ]
  [ ! -s "$WORK/bins2/manifest" ]
  run env PATH="$WORK/stub:$PATH" TEST_BINARIES_ONLY="$WORK/missing.txt" bash repo/scripts/ci/test-binaries.sh "$WORK/bins3" internal
  [ "$status" -eq 1 ]
}

# ── bazel-remote-cache.sh ───────────────────────────────────────────────────

rcache() { # EVENT REF — the script against a fresh rc file
  rm -f "$WORK/rc"
  RUNNER_TEMP="$WORK" run bash "$CI_DIR/bazel-remote-cache.sh" "$1" "$2" "$WORK/rc"
}

@test "remote cache: no URL changes nothing" {
  unset BAZEL_REMOTE_CACHE_URL
  rcache push refs/heads/main
  [ "$status" -eq 0 ]
  [[ "$output" == "remote-cache=off"* ]]
  [ ! -e "$WORK/rc" ]
}

@test "remote cache: a pull request reads, never writes — even handed the write credential" {
  export BAZEL_REMOTE_CACHE_URL=grpcs://cache.example:443
  BAZEL_REMOTE_CACHE_READ_HEADER='Authorization=Basic cmVhZA==' rcache pull_request refs/pull/1/merge
  [[ "$output" == "remote-cache=read-only"* ]]
  grep -qx 'build --remote_cache=grpcs://cache.example:443' "$WORK/rc"
  grep -qx 'build --remote_upload_local_results=false' "$WORK/rc"
  grep -qx 'build --remote_header=Authorization=Basic cmVhZA==' "$WORK/rc"
  BAZEL_REMOTE_CACHE_WRITE_HEADER='Authorization=Basic d3JpdGU=' rcache pull_request refs/pull/1/merge
  [[ "$output" == "remote-cache=off"* ]]
  [ ! -e "$WORK/rc" ]
  BAZEL_REMOTE_CACHE_WRITE_HEADER='Authorization=Basic d3JpdGU=' BAZEL_REMOTE_CACHE_READ=anonymous rcache push refs/heads/feature
  [[ "$output" == "remote-cache=read-only"* ]]
  run grep -c 'remote_header' "$WORK/rc"
  [ "$output" = 0 ]
  grep -qx 'build --remote_upload_local_results=false' "$WORK/rc"
}

@test "remote cache: main, the queue and the nightly run write with the write credential" {
  export BAZEL_REMOTE_CACHE_URL=https://cache.example
  for ev in "push refs/heads/main" "merge_group refs/heads/gh-readonly-queue/main/pr-1-a" "schedule refs/heads/main"; do
    # shellcheck disable=SC2086
    BAZEL_REMOTE_CACHE_WRITE_HEADER='Authorization=Basic d3JpdGU=' rcache $ev
    [[ "$output" == "remote-cache=read-write"* ]] || { echo "$ev: $output"; return 1; }
    grep -qx 'build --remote_upload_local_results=true' "$WORK/rc"
    grep -qx 'build --remote_header=Authorization=Basic d3JpdGU=' "$WORK/rc"
  done
  BAZEL_REMOTE_CACHE_CLIENT_CERT=CERT BAZEL_REMOTE_CACHE_CLIENT_KEY=KEY rcache schedule refs/heads/main
  [[ "$output" == "remote-cache=read-write"* ]]
  grep -qx "build --tls_client_certificate=$WORK/bazel-remote-cache/client.crt" "$WORK/rc"
  [ "$(cat "$WORK/bazel-remote-cache/client.key")" = KEY ]
  # Without a credential, main reads at most.
  rcache push refs/heads/main
  [[ "$output" == "remote-cache=off"* ]]
}

@test "remote cache: a malformed configuration turns it off, never fails the run" {
  BAZEL_REMOTE_CACHE_URL=cache.example:443 BAZEL_REMOTE_CACHE_READ=anonymous rcache pull_request x
  [ "$status" -eq 0 ]
  grep -qx 'remote-cache=off' <<<"$output"
  [ ! -e "$WORK/rc" ]
  BAZEL_REMOTE_CACHE_URL=https://c BAZEL_REMOTE_CACHE_READ_HEADER=$'A=b\nbuild --x' rcache pull_request x
  grep -qx 'remote-cache=off' <<<"$output"
  BAZEL_REMOTE_CACHE_URL=https://c BAZEL_REMOTE_CACHE_READ_HEADER=nonamevalue rcache pull_request x
  grep -qx 'remote-cache=off' <<<"$output"
  BAZEL_REMOTE_CACHE_URL=https://c BAZEL_REMOTE_CACHE_CLIENT_CERT=C rcache schedule x
  grep -qx 'remote-cache=off' <<<"$output"
  [ ! -e "$WORK/rc" ]
  run bash "$CI_DIR/bazel-remote-cache.sh" push
  [ "$status" -eq 64 ]
}

# ── the workflows' shape ────────────────────────────────────────────────────

# job_body <workflow> <job> — the lines of one job.
job_body() {
  awk -v job="  $2:" '
    $0 == job { grab = 1; next }
    grab && /^  [A-Za-z0-9_-]+:/ { exit }
    grab && /^[^[:space:]#]/ { exit }
    grab { print }
  ' "$1"
}

# on_block <workflow> — the `on:` block.
on_block() { awk '/^on:/ { p = 1; next } p && /^[^ #]/ { exit } p { print }' "$1"; }

@test "workflows: the queue, main, the nightly and a manual run reach both, the nightly in a group of its own" {
  for wf in "$BAZEL_CI" "$E2E"; do
    block="$(on_block "$wf")"
    [[ "$block" == *"  schedule:"* ]]
    [[ "$block" == *"- cron: '"* ]]
    [[ "$block" == *"  workflow_dispatch:"* ]]
    grep -q "github.event_name == 'schedule' && 'nightly' || github.ref" "$wf"
  done
  block="$(on_block "$BAZEL_CI")"
  [[ "$block" == *"  pull_request:"* ]]
  [[ "$block" == *"  merge_group:"* ]]
}

# A lane reading the plan runs unless the plan says skip: a plan that failed
# answers nothing, and the lane runs whole. `needs:` alone would skip it.
@test "workflows: a scoped lane skips only on the plan's explicit skip" {
  check() { # wf job output
    local body
    body="$(job_body "$1" "$2")"
    [[ "$body" == *"    needs: scope"* ]] || { echo "$2 does not need scope"; return 1; }
    [[ "$body" == *"    if: \${{ !cancelled() && needs.scope.outputs.$3 != 'skip' }}"* ]] || { echo "$2: its if"; return 1; }
  }
  check "$BAZEL_CI" test-386 test386
  check "$BAZEL_CI" test-simd simd
  check "$BAZEL_CI" garble-tiny garble
  check "$E2E" native native
  check "$E2E" linux-init linux_init
  check "$E2E" bsd bsd
  check "$E2E" solarish solarish
  # No other job of either workflow reads a skip.
  run grep -c "!= 'skip'" "$BAZEL_CI"
  [ "$output" = 3 ]
  run grep -c "!= 'skip'" "$E2E"
  [ "$output" = 4 ]
}

# The plan is never a gate of the required job: it runs whatever the plan's
# result, and its scope step reads that result before the plan.
@test "workflows: the required bazel job runs whole when the plan failed" {
  body="$(job_body "$BAZEL_CI" bazel)"
  [[ "$body" == *"    needs: [shell-gates, lint-ktn, models, scope]"* ]]
  [[ "$body" == *'    if: ${{ !cancelled() }}'* ]]
  [[ "$body" == *'SCOPE_RESULT: ${{ needs.scope.result }}'* ]]
  [[ "$body" != *'needs.scope.result != success'* ]]
  # Every scoped step runs //... unless its mode is scoped.
  for cmd in 'bazel build --config=ci //...' 'bazel test --config=ci //...' 'bazel coverage --combined_report=lcov //...' \
    'bazel test --config=alloc "${targets[@]}"' 'make test-framework'; do
    [[ "$body" == *"$cmd"* ]] || { echo "missing: $cmd"; return 1; }
  done
  run grep -c 'if \[ "$MODE" != scoped \]; then' <<<"$body"
  [ "$output" = 4 ]
  run grep -c 'if \[ "$MODE" = scoped \] && bash scripts/ci/scope.sh affected' <<<"$body"
  [ "$output" = 1 ]
}

# Never `latest`: a tool that moves changes the verdict with no commit here.
# A scope that fails runs everything; a checkout no longer the commit under
# test fails the required job; a remote cache that cannot be set runs without.
@test "workflows: the Bazel scope and the remote cache never fail the job, but a moved checkout does" {
  body="$(job_body "$BAZEL_CI" bazel)"
  [[ "$body" == *'2) echo "::error::the checkout is no longer the commit under test"; exit 1 ;;'* ]]
  [[ "$body" == *'echo "mode=fallback" >>"$GITHUB_OUTPUT"'* ]]
  [[ "$body" == *'echo "remote-cache=off" >>"$GITHUB_OUTPUT"'* ]]
}

@test "workflows: target-determinator is pinned by version and SHA-256" {
  body="$(job_body "$BAZEL_CI" bazel)"
  [[ "$body" =~ TD_VERSION:\ v[0-9]+\.[0-9]+\.[0-9]+ ]]
  [[ "$body" =~ TD_SHA256:\ [0-9a-f]{64} ]]
  grep -q 'sha256sum -c' "$CI_DIR/scope.sh"
}

# The write credential reaches the step on the events that may write, and
# no other; the read credential and the URL on every event.
@test "workflows: the remote cache's write credential is handed out on main, the queue and the nightly alone" {
  body="$(job_body "$BAZEL_CI" bazel)"
  guard="(github.event_name == 'merge_group' || github.event_name == 'schedule' || (github.event_name == 'push' && github.ref == 'refs/heads/main'))"
  for s in BAZEL_REMOTE_CACHE_WRITE_HEADER BAZEL_REMOTE_CACHE_CLIENT_CERT BAZEL_REMOTE_CACHE_CLIENT_KEY; do
    line="$(grep "          $s:" <<<"$body")"
    [[ "$line" == *"$guard && secrets.$s || ''"* ]] || { echo "$s: $line"; return 1; }
  done
  run grep -c 'secrets.BAZEL_REMOTE_CACHE_WRITE_HEADER' "$BAZEL_CI"
  [ "$output" = 1 ]
  [[ "$body" == *'bash scripts/ci/bazel-remote-cache.sh "$EVENT" "$REF" "$HOME/.bazelrc"'* ]]
}

@test "workflows: each summary needs every lane, always judges, and names each" {
  body="$(job_body "$BAZEL_CI" scope-summary)"
  [[ "$body" == *"    needs: [scope, bazel, shell-gates, lint-ktn, models, cross-build, test-386, test-simd, garble-tiny, docs-site]"* ]]
  [[ "$body" == *'    if: ${{ !cancelled() }}'* ]]
  [[ "$body" == *"bash scripts/ci/scope.sh summary"* ]]
  body="$(job_body "$E2E" scope-summary)"
  [[ "$body" == *"    needs: [scope, native, linux-init, bsd, solarish]"* ]]
  [[ "$body" == *'    if: ${{ !cancelled() }}'* ]]
}

@test "workflows: each plan names its own workflow and scripts/ci/ as files that run everything" {
  run grep -cF "'^\.github/workflows/bazel-ci\.yml\$' '^scripts/ci/' '^Makefile\$'" "$BAZEL_CI"
  [ "$output" = 1 ]
  run grep -cF "'^\.github/workflows/e2e-cross\.yml\$' '^scripts/ci/'" "$E2E"
  [ "$output" = 1 ]
}

# ── the readers table against Bazel's ───────────────────────────────────────

# Bazel's sandbox lets a test read only what its BUILD file declares as data,
# so every cross-package `data` label is a file a test reads outside its own
# package — exactly what `go test` reads with no import saying so. Each must
# be a reader rule naming that package, or a change to the file skips the test
# that reads it.
@test "readers: every cross-package data label of a BUILD file is a reader rule" {
  rules="$(bash "$CI_DIR/scope.sh" rules | awk '/^# readers/ { p = 1; next } /^# / { p = 0 } p')"
  [ -n "$rules" ]
  labels="$(cd "$REPO_ROOT" && git ls-files '*BUILD.bazel' | while IFS= read -r f; do
    awk -v f="$f" '
      /^    [a-z_]+ = / { indata = ($0 ~ /^    data = /) }
      /^\)/ { indata = 0 }
      indata {
        line = $0
        while (match(line, /"\/\/[^"]*"/)) {
          print f "\t" substr(line, RSTART + 1, RLENGTH - 2)
          line = substr(line, RSTART + RLENGTH)
        }
      }' "$REPO_ROOT/$f"
  done)"
  [ -n "$labels" ]
  n=0
  while IFS=$'\t' read -r build label; do
    pkg="$(dirname "$build")"
    lpkg="${label#//}"
    lpkg="${lpkg%%:*}"
    name="${label#*:}"
    [ "$lpkg" = "${pkg#.}" ] && continue
    case "$name" in
      audit_sources | audit_srcs) path="${lpkg:+$lpkg/}x.go" ;;
      *.*) path="${lpkg:+$lpkg/}$name" ;;
      *) echo "$build: $label is a target this suite cannot map to a path" >&2; return 1 ;;
    esac
    hit=0
    while IFS=$'\t' read -r re readers; do
      if [[ "$path" =~ $re ]] && [[ " $readers " == *" $pkg "* ]]; then hit=1; break; fi
    done <<<"$rules"
    [ "$hit" -eq 1 ] || { echo "$build reads $label ($path), and no reader rule names $pkg" >&2; return 1; }
    n=$((n + 1))
  done <<<"$labels"
  [ "$n" -ge 10 ]
}

# The brief's list, as ADR 0250 records it: none of these may be scoped.
@test "readers: the build's definition is in the fallback rules" {
  rules="$(bash "$CI_DIR/scope.sh" rules | awk '/^# fallback/ { p = 1; next } /^# / { p = 0 } p' | cut -f1)"
  for path in MODULE.bazel MODULE.bazel.lock .bazelrc .bazelversion BUILD.bazel WORKSPACE WORKSPACE.bzlmod \
    go.mod go.sum internal/go.mod third-party/aws/go.sum go.work go.work.sum tools/genindex/main.go; do
    hit=0
    while IFS= read -r re; do [[ "$path" =~ $re ]] && hit=1; done <<<"$rules"
    [ "$hit" -eq 1 ] || { echo "$path is not a fallback" >&2; return 1; }
  done
}
