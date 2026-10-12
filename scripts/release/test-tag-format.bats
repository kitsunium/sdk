#!/usr/bin/env bats
# BATS tests for lib/tag-format.sh's module shapes (ADR 0171): a module per pkg
# family, `pkg/v1/<family>/vX.Y.Z`, and one shared internal module,
# `internal/vX.Y.Z` — anchored at both ends, so no selection by glob or prefix
# reads one as pkg's history or as a tombstone —, the module a tag belongs to,
# the latest tag of one module, and the remote's tags read without the peeled
# `^{}` lines. The older shapes are pinned in test-cut-tags.bats.

g() { git -c core.hooksPath="$NOHOOKS" "$@"; }

setup() {
  NOHOOKS="$BATS_TEST_TMPDIR/nohooks"
  mkdir -p "$NOHOOKS"
  LIB="$BATS_TEST_DIRNAME/lib/tag-format.sh"
  REPO="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$REPO"
  cd "$REPO"
  g init -q -b main
  g config user.email "ci@example.invalid"
  g config user.name "ci"
  : >f
  g add f
  g commit -q --no-verify -m init
}

# lib <function> <args…> — run one function of the library in a fresh shell.
lib() {
  bash -c '. "$1"; shift; "$@"' _ "$LIB" "$@"
}

@test "a family module's tag is pkg/v1/<family>/vX.Y.Z, anchored" {
  for t in pkg/v1/data/v0.1.0 pkg/v1/app/v1.2.3 pkg/v1/net/v0.1.0-rc.1 pkg/v1/x2/v0.0.1; do
    run lib is_valid_family_tag "$t"
    [ "$status" -eq 0 ] || { echo "refused $t"; return 1; }
    run lib is_valid_release_tag "$t"
    [ "$status" -eq 0 ] || { echo "not a release tag: $t"; return 1; }
  done
  for t in pkg/v1/data/v2.0.0 pkg/v1/Data/v0.1.0 pkg/v1/v2/v0.1.0 pkg/v1/data/sub/v0.1.0 \
    xpkg/v1/data/v0.1.0 pkg/v1/data/v0.1.0x pkg/v2/data/v0.1.0 pkg/v1/v0.1.0 'pkg/v1/data/v0.1.0;rm' pkg/v1//v0.1.0; do
    run lib is_valid_family_tag "$t"
    [ "$status" -ne 0 ] || { echo "accepted $t"; return 1; }
  done
}

@test "the internal module's tag is internal/vX.Y.Z, never a layer's" {
  for t in internal/v0.1.0 internal/v1.0.0-rc.2; do
    run lib is_valid_internal_tag "$t"
    [ "$status" -eq 0 ]
    run lib is_valid_release_tag "$t"
    [ "$status" -eq 0 ]
  done
  for t in internal/core/v0.1.0 internal/kernel/v0.17.0 internal/v2.0.0 xinternal/v0.1.0; do
    run lib is_valid_internal_tag "$t"
    [ "$status" -ne 0 ] || { echo "accepted $t"; return 1; }
    run lib is_valid_release_tag "$t"
    [ "$status" -ne 0 ] || { echo "release tag: $t"; return 1; }
  done
}

@test "a module tag is neither pkg's history nor a tombstone" {
  for t in pkg/v1/data/v0.1.0 internal/v0.18.0; do
    run lib is_valid_pkg_tag "$t"
    [ "$status" -ne 0 ]
    run lib is_valid_tombstone_tag "$t"
    [ "$status" -ne 0 ]
    run lib is_valid_tag "$t"
    [ "$status" -ne 0 ]
  done
}

@test "module_tag_dir names the module of a release tag, and refuses the rest" {
  run lib module_tag_dir v0.19.8
  [ "$output" = "." ]
  run lib module_tag_dir pkg/v1/data/v0.1.0
  [ "$output" = "pkg/v1/data" ]
  run lib module_tag_dir internal/v0.1.0
  [ "$output" = "internal" ]
  run lib module_tag_dir third-party/db/writer/mysql/v0.19.6
  [ "$output" = "third-party/db/writer/mysql" ]
  for t in pkg/v0.18.0 framework/v0.18.0 internal/core/v0.1.0 pin/sdk-api-0eb4ef16; do
    run lib module_tag_dir "$t"
    [ "$status" -ne 0 ] || { echo "named a module for $t"; return 1; }
  done
}

@test "is_module_dir accepts the layout's directories and nothing else" {
  for d in . pkg/v1/data internal third-party/aws framework/connectors/mysql; do
    run lib is_module_dir "$d"
    [ "$status" -eq 0 ] || { echo "refused $d"; return 1; }
  done
  for d in pkg framework internal/core pkg/v1 pkg/v1/data/sub tools/genindex; do
    run lib is_module_dir "$d"
    [ "$status" -ne 0 ] || { echo "accepted $d"; return 1; }
  done
}

@test "latest_module_tag reads exactly one module's tags" {
  for t in pkg/v1/data/v0.1.0 pkg/v1/data/v0.2.0 pkg/v1/data/v0.10.0 pkg/v1/datax/v0.9.0 \
    pkg/v1/data/v0.11.0-rc.1 v0.19.7 internal/v0.3.0 pkg/v0.18.0; do
    g tag "$t"
  done
  run lib latest_module_tag pkg/v1/data
  [ "$output" = "pkg/v1/data/v0.10.0" ]
  run lib latest_module_tag pkg/v1/datax
  [ "$output" = "pkg/v1/datax/v0.9.0" ]
  run lib latest_module_tag internal
  [ "$output" = "internal/v0.3.0" ]
  run lib latest_module_tag pkg/v1/net
  [ -z "$output" ]
  # The root reads its own history: a module tag never sorts above it, and the
  # pkg/ glob does not pick a family's tag up as pkg's history.
  run lib latest_module_tag .
  [ "$output" = "v0.19.7" ]
  run lib latest_pkg_tag
  [ "$output" = "pkg/v0.18.0" ]
}

@test "version_sort orders module tags by their version" {
  run bash -c '. "$1"; printf "pkg/v1/data/v0.10.0\npkg/v1/data/v0.9.0\npkg/v1/data/v0.9.1\n" | version_sort' _ "$LIB"
  [ "$output" = "$(printf 'pkg/v1/data/v0.9.0\npkg/v1/data/v0.9.1\npkg/v1/data/v0.10.0')" ]
}

@test "remote_tags_present compares full refs and drops the peeled lines" {
  REMOTE="$BATS_TEST_TMPDIR/remote.git"
  git init -q --bare "$REMOTE"
  g tag -a v0.1.0 -m annotated
  g tag -a pkg/v1/data/v0.2.0 -m annotated
  g tag internal/v0.1.0
  g push -q "$REMOTE" --tags
  # The remote lists v0.1.0's peeled line too: ls-remote without --refs does.
  run git ls-remote --tags "$REMOTE"
  [[ "$output" == *'refs/tags/v0.1.0^{}'* ]]
  run lib remote_tags_present "$REMOTE" v0.1.0 v0.2.0 pkg/v1/data/v0.2.0 internal/v0.1.0 data/v0.2.0
  [ "$status" -eq 0 ]
  # v0.2.0 and data/v0.2.0 are tails of pkg/v1/data/v0.2.0, not tags.
  [ "$output" = "$(printf 'v0.1.0\npkg/v1/data/v0.2.0\ninternal/v0.1.0')" ]
}

@test "remote_tags_present refuses a remote it cannot read" {
  run lib remote_tags_present "$BATS_TEST_TMPDIR/nowhere.git" v0.1.0
  [ "$status" -eq 2 ]
  [[ "$output" == *"git ls-remote"*"failed"* ]]
}
