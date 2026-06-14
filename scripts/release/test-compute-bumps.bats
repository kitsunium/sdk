#!/usr/bin/env bats
# BATS tests for compute-bumps.sh. Each test stands up a disposable git repo so
# the assertions are reproducible offline. No Bazel needed — when bazel is
# absent the internal/* path falls back to a no-op (compute-bumps gracefully
# skips the rdeps step). The public module is the bare `pkg`, so the script
# emits the single token "pkg" (never per-major "vN").

setup() {
  REPO="$(mktemp -d)"
  cd "$REPO"
  git init -q -b main
  git config user.email "ci@example.invalid"
  git config user.name  "ci"
  mkdir -p pkg/v1 internal/kernel/errs
  cat >pkg/go.mod <<'EOF'
module github.com/kitsunium/sdk/pkg

go 1.26
EOF
  : > pkg/v1/codec.go
  : > internal/kernel/errs/errs.go
  git add -A
  git commit -q -m "init"
  git tag pkg/v0.1.0

  SCRIPT="$BATS_TEST_DIRNAME/compute-bumps.sh"
}

teardown() { rm -rf "$REPO"; }

@test "no changes since last tag emits nothing" {
  run "$SCRIPT" --range="pkg/v0.1.0..HEAD"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "change under pkg/v1 emits pkg" {
  echo "// patch" >> pkg/v1/codec.go
  git commit -aq -m "feat(v1): tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

@test "change to pkg/go.mod emits pkg" {
  printf '\n// bump\n' >> pkg/go.mod
  git commit -aq -m "chore: module tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
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
  [ "$output" = "pkg" ]
}

@test "empty repo with no pkg dirs emits nothing" {
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

# Regression: the release tag lives on a DETACHED child of main (how cut-tags.sh
# publishes it), so `git describe` can't see it. compute-bumps must baseline on
# the tag's first parent, not fall back to root..HEAD and emit a spurious bump.
@test "detached release tag + no new main commits emits nothing (no spurious bump)" {
  cd "$(mktemp -d)"
  git init -q -b main
  git config user.email "ci@example.invalid"; git config user.name "ci"
  mkdir -p pkg/v1
  printf 'module github.com/kitsunium/sdk/pkg\n\ngo 1.26\n' > pkg/go.mod
  : > pkg/v1/codec.go
  git add -A; git commit -q -m "init"
  base="$(git rev-parse HEAD)"
  # cut-tags-style: a detached commit whose parent is main HEAD, tagged, not on a branch.
  rel="$(git commit-tree "HEAD^{tree}" -p "$base" -m "release v0.1.0")"
  git tag pkg/v0.1.0 "$rel"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "detached release tag + a real pkg change after the base emits pkg" {
  cd "$(mktemp -d)"
  git init -q -b main
  git config user.email "ci@example.invalid"; git config user.name "ci"
  mkdir -p pkg/v1
  printf 'module github.com/kitsunium/sdk/pkg\n\ngo 1.26\n' > pkg/go.mod
  : > pkg/v1/codec.go
  git add -A; git commit -q -m "init"
  base="$(git rev-parse HEAD)"
  rel="$(git commit-tree "HEAD^{tree}" -p "$base" -m "release v0.1.0")"
  git tag pkg/v0.1.0 "$rel"
  echo "// new" >> pkg/v1/codec.go
  git commit -aq -m "feat(v1): real change after release"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}
