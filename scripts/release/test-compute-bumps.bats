#!/usr/bin/env bats
# BATS tests for compute-bumps.sh (plan A4). Each test stands up a
# disposable git repo so the assertions are reproducible offline. No
# Bazel needed — when bazel is absent the internal/* path falls back
# to a no-op (compute-bumps gracefully skips the rdeps step).

setup() {
  REPO="$(mktemp -d)"
  cd "$REPO"
  git init -q -b main
  git config user.email "ci@example.invalid"
  git config user.name  "ci"
  mkdir -p pkg/v1 pkg/v2 internal/kernel/errs
  : > pkg/v1/codec.go
  : > pkg/v2/codec.go
  : > internal/kernel/errs/errs.go
  git add -A
  git commit -q -m "init"
  git tag pkg/v1/v1.0.0
  git tag pkg/v2/v2.0.0

  SCRIPT="$BATS_TEST_DIRNAME/compute-bumps.sh"
}

teardown() { rm -rf "$REPO"; }

@test "no changes since last tag emits nothing" {
  run "$SCRIPT" --range="pkg/v1/v1.0.0..HEAD"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "change under pkg/v1 bumps v1 only" {
  echo "// patch" >> pkg/v1/codec.go
  git commit -aq -m "feat(v1): tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "v1" ]
}

@test "change under pkg/v2 bumps v2 only" {
  echo "// patch" >> pkg/v2/codec.go
  git commit -aq -m "feat(v2): tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "v2" ]
}

@test "mixed pkg/v1 and pkg/v2 change bumps both" {
  echo "// patch" >> pkg/v1/codec.go
  echo "// patch" >> pkg/v2/codec.go
  git commit -aq -m "feat: tweak both"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | tr '\n' ' ')" = "v1 v2 " ]
}

@test "bootstrap repo with no tags uses root commit as fallback" {
  cd "$(mktemp -d)"
  git init -q -b main
  git config user.email "ci@example.invalid"
  git config user.name  "ci"
  mkdir -p pkg/v1
  : > pkg/v1/x.go
  git add -A
  git commit -q -m "first"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "v1" ]
}

@test "empty repo with no pkg/v* dirs emits nothing (nullglob)" {
  cd "$(mktemp -d)"
  git init -q -b main
  git config user.email "ci@example.invalid"
  git config user.name  "ci"
  : > README.md
  git add -A
  git commit -q -m "first"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "internal-only change without bazel emits nothing (graceful)" {
  if command -v bazel >/dev/null 2>&1; then skip "bazel present, rdeps path active"; fi
  echo "// tweak" >> internal/kernel/errs/errs.go
  git commit -aq -m "fix(errs): wording"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}
