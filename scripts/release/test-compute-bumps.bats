#!/usr/bin/env bats
# BATS tests for compute-bumps.sh. Each test stands up a disposable git repo so
# the assertions are reproducible offline. No Bazel needed — when bazel is
# absent the internal/* path falls back to a no-op (compute-bumps gracefully
# skips the rdeps step). The public module is the bare `pkg`, so the script
# emits the single token "pkg" (never per-major "vN").

# Every commit here is --no-verify. The fixtures are throwaway repositories, but
# `core.hooksPath` is inherited from the developer's own git config, so without
# it a host pre-commit hook runs inside them and its refusal fails the suite in
# `setup` on code that is correct. Neither a repo-local `core.hooksPath` nor
# GIT_CONFIG_GLOBAL=/dev/null is enough — a hooks path injected at `-c`
# precedence outranks both; only the command-line flag does (ADR 0088).
# `git commit-tree` is plumbing and runs no hooks, so it is left alone.
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
  git commit -q --no-verify -m "init"
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
  git commit -aq --no-verify -m "feat(v1): tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

@test "change to pkg/go.mod emits pkg" {
  printf '\n// bump\n' >> pkg/go.mod
  git commit -aq --no-verify -m "chore: module tweak"
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
  git commit -q --no-verify -m "first"
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
  git commit -q --no-verify -m "first"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "internal-only change without bazel emits nothing (graceful)" {
  if command -v bazel >/dev/null 2>&1; then skip "bazel present, rdeps path active"; fi
  echo "// tweak" >> internal/kernel/errs/errs.go
  git commit -aq --no-verify -m "fix(errs): wording"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# The rdeps step was `bazel query … | grep -q .` inside an `if`. `grep -q .`
# matches the FIRST line and exits, bazel takes SIGPIPE, `pipefail` reports 141,
# and the `if` is FALSE — so a query that DID reach //pkg/... left need_bump at 0
# and cut no release at all. Measured with a stub emitting 608 KB: rc=141,
# `if` false. The stub is what makes this testable without a Bazel workspace, and
# a real rdeps over this repository is far larger than the pipe buffer.
@test "a large rdeps answer still counts as reaching pkg" {
  stub="$(mktemp -d)"
  cat >"$stub/bazel" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  query) for i in $(seq 1 20000); do echo "//internal/kernel/pkg$i:lib"; done ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$stub/bazel"
  echo "// tweak" >> internal/kernel/errs/errs.go
  git commit -aq --no-verify -m "fix(errs): wording"
  PATH="$stub:$PATH" run "$SCRIPT"
  rm -rf "$stub"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
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
  git add -A; git commit -q --no-verify -m "init"
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
  git add -A; git commit -q --no-verify -m "init"
  base="$(git rev-parse HEAD)"
  rel="$(git commit-tree "HEAD^{tree}" -p "$base" -m "release v0.1.0")"
  git tag pkg/v0.1.0 "$rel"
  echo "// new" >> pkg/v1/codec.go
  git commit -aq --no-verify -m "feat(v1): real change after release"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}
