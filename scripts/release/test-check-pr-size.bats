#!/usr/bin/env bats
# BATS tests for check-pr-size.sh — the pull-request half of ADR 0135. GitHub
# is the gh stub from test-helpers.bash: each test writes the three answers a
# pull request has (its files, its labels, its commits) and asserts the verdict
# the check gives BEFORE the merge, which is the verdict cut-tags.sh would give
# after it.

load test-helpers

setup() {
  install_gh_stub
  SCRIPT="$BATS_TEST_DIRNAME/check-pr-size.sh"
}

# pr_files <n> <path…> — the files pull request <n> changes.
pr_files() {
  local n="$1"
  shift
  jq -n '$ARGS.positional | map({filename: .})' --args "$@" >"$GH_FIXTURES/pulls_${n}_files.json"
}

# pr_labels <n> [label…] — the labels pull request <n> carries.
pr_labels() {
  local n="$1"
  shift
  jq -n '$ARGS.positional | map({name: .})' --args "$@" >"$GH_FIXTURES/issues_${n}_labels.json"
}

# pr_commits <n> <message…> — the commit messages of pull request <n>'s branch.
pr_commits() {
  local n="$1"
  shift
  jq -n '$ARGS.positional | map({commit: {message: .}})' --args "$@" >"$GH_FIXTURES/pulls_${n}_commits.json"
}

@test "a pull request touching nothing releasable sizes nothing, and is not asked" {
  pr_files 1 docs/adr/0135-x.md .github/workflows/x.yml
  pr_labels 1
  pr_commits 1 $'docs: notes\n\nRelease-bump: minor'
  run "$SCRIPT" --pr=1
  [ "$status" -eq 0 ]
  [[ "$output" == *"touches nothing that can cut a release"* ]]
}

# The case the merge would refuse: a request nobody with the authority decided.
@test "an unlabelled pull request whose commits ask for a minor fails, naming the fix" {
  pr_files 2 pkg/v1/codec/codec.go
  pr_labels 2 bug
  pr_commits 2 'feat(codec): one' $'feat(codec): two\n\nRelease-bump: minor'
  run "$SCRIPT" --pr=2
  [ "$status" -eq 1 ]
  [[ "$output" == *"asks for 'Release-bump: minor'"* ]]
  [[ "$output" == *"release:minor    to grant the request"* ]]
  [[ "$output" == *"release:patch    to decline it"* ]]
}

@test "release:minor grants the request" {
  pr_files 3 internal/service/svc.go
  pr_labels 3 release:minor
  pr_commits 3 $'feat(service): behaviour\n\nRelease-bump: minor'
  run "$SCRIPT" --pr=3
  [ "$status" -eq 0 ]
  [[ "$output" == *"released as a minor (label release:minor)"* ]]
}

# #224 at the door: a contributor's request is declined with a label, not by
# rewriting the contributor's commits.
@test "release:patch declines a contributor's request" {
  pr_files 4 pkg/v1/codec/codec.go
  pr_labels 4 release:patch
  pr_commits 4 $'feat(codec): proposal\n\nRelease-bump: minor'
  run "$SCRIPT" --pr=4
  [ "$status" -eq 0 ]
  [[ "$output" == *"the label decides"* ]]
  [[ "$output" == *"released as a patch (label release:patch)"* ]]
}

# Every branch commit, and anywhere in it: the squash folds them all into the
# message the release reads, so a request in the middle of the first commit is
# as much a request as one at the end of the last.
@test "a request in the middle of any branch commit counts" {
  pr_files 5 pkg/v1/codec/codec.go
  pr_labels 5
  pr_commits 5 $'feat(codec): one\n\nRelease-bump: minor\n\nProse after it.' 'fix(codec): two'
  run "$SCRIPT" --pr=5
  [ "$status" -eq 1 ]
}

@test "an unlabelled pull request that asks for nothing is a patch" {
  pr_files 6 pkg/v1/codec/codec.go
  pr_labels 6
  pr_commits 6 'fix(codec): one'
  run "$SCRIPT" --pr=6
  [ "$status" -eq 0 ]
  [[ "$output" == *"released as a patch (no release label"* ]]
}

@test "two release labels fail" {
  pr_files 7 pkg/v1/codec/codec.go
  pr_labels 7 release:minor release:major
  pr_commits 7 'feat(codec): one'
  run "$SCRIPT" --pr=7
  [ "$status" -eq 1 ]
  [[ "$output" == *"two release labels"* ]]
}

# The file list is what decides whether the size is asked at all, so a list
# that could not be read must never pass as an empty one.
@test "a pull request GitHub could not list fails, never passes" {
  : >"$GH_FIXTURES/pulls_8_files.fail"
  run "$SCRIPT" --pr=8
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not read the files of #8"* ]]
}

@test "the commits GitHub could not list fail, never pass" {
  pr_files 9 pkg/v1/codec/codec.go
  pr_labels 9
  : >"$GH_FIXTURES/pulls_9_commits.fail"
  run "$SCRIPT" --pr=9
  [ "$status" -eq 2 ]
}

@test "a missing gh fails" {
  RELEASE_GH="$BATS_TEST_TMPDIR/no-such-gh" run "$SCRIPT" --pr=10
  [ "$status" -eq 2 ]
  [[ "$output" == *"is not on PATH"* ]]
}

@test "--pr is required and must be a number" {
  run "$SCRIPT"
  [ "$status" -eq 64 ]
  run "$SCRIPT" --pr=12abc
  [ "$status" -eq 64 ]
}
