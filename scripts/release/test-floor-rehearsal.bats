#!/usr/bin/env bats
# BATS tests for check-version-floor.sh — no module path tagged at or below the
# highest version the proxy lists for it (ADR 0171 §2) — against a FILE proxy,
# GOPROXY's own layout, so no case needs the network; and for
# proxy-rehearsal.sh's fixture, which must build the consumer pinned to the
# monolithic root AND see its control fail with an ambiguous import.

g() { git -c core.hooksPath="$NOHOOKS" "$@"; }

setup() {
  NOHOOKS="$BATS_TEST_TMPDIR/nohooks"
  mkdir -p "$NOHOOKS"
  FLOOR="$BATS_TEST_DIRNAME/check-version-floor.sh"
  REHEARSAL="$BATS_TEST_DIRNAME/proxy-rehearsal.sh"
  PROXY="$BATS_TEST_TMPDIR/proxy"
  REPO="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$REPO" "$PROXY"
  cd "$REPO"
  g init -q -b main
  # Every commit and every push spawns `git maintenance run --auto --detach`
  # (git 2.47 measured), which outlives the command and takes
  # objects/maintenance.lock: a teardown's `rm -rf` racing it fails with
  # "objects: Directory not empty". A disposable repository needs no upkeep.
  g config maintenance.auto false
  g config user.email "ci@example.invalid"
  g config user.name "ci"
  mkdir -p third-party/aws pkg/v1/data
  printf 'module github.com/kitsunium/sdk\n\ngo 1.27\n' >go.mod
  printf 'module github.com/kitsunium/sdk/third-party/aws\n\ngo 1.27\n' >third-party/aws/go.mod
  printf 'module github.com/kitsunium/sdk/pkg/v1/data\n\ngo 1.27\n' >pkg/v1/data/go.mod
  printf 'go 1.27\n\nuse (\n\t.\n\t./pkg/v1/data\n\t./third-party/aws\n)\n' >go.work
  g add -A
  g commit -q --no-verify -m init
}

# published <module> <version…> — what the file proxy lists for <module>.
published() {
  local m="$1"
  shift
  mkdir -p "$PROXY/$m/@v"
  printf '%s\n' "$@" >"$PROXY/$m/@v/list"
}

floor() {
  bash "$FLOOR" --proxy="file://$PROXY" "$@"
}

@test "a tag above the published maximum clears the floor; at or below it does not" {
  published github.com/kitsunium/sdk v0.0.0 v0.18.0 v0.19.6 v0.19.7
  run floor v0.19.8 v0.20.0
  [ "$status" -eq 0 ]
  for t in v0.19.7 v0.19.6 v0.1.0 v0.19.7-rc.1; do
    run floor "$t"
    [ "$status" -eq 1 ] || { echo "cleared $t"; return 1; }
    [[ "$output" == *"$t is at or below v0.19.7"* ]]
  done
}

@test "a never-published path may start at v0.1.0; a published one may not" {
  published github.com/kitsunium/sdk/third-party/aws v0.18.0 v0.19.6
  run floor pkg/v1/data/v0.1.0 internal/v0.1.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"github.com/kitsunium/sdk/pkg/v1/data was never published"* ]]
  run floor third-party/aws/v0.1.0
  [ "$status" -eq 1 ]
  run floor third-party/aws/v0.19.7
  [ "$status" -eq 0 ]
}

# The default question: the clone is not behind the proxy, so the next cut —
# above the clone's last tag — is above the proxy's too.
@test "with no tag, every module's last tag here must reach the proxy's maximum" {
  published github.com/kitsunium/sdk v0.19.7
  published github.com/kitsunium/sdk/third-party/aws v0.19.6
  g tag v0.19.7
  g tag third-party/aws/v0.19.6
  run floor
  [ "$status" -eq 0 ]
  [[ "$output" == *"ok pkg/v1/data — never published, starts at v0.1.0"* ]]
  # A tag this clone does not hold, published: the next cut would go below it.
  published github.com/kitsunium/sdk/third-party/aws v0.19.6 v0.19.9
  run floor
  [ "$status" -eq 1 ]
  [[ "$output" == *"latest tag here is third-party/aws/v0.19.6, below v0.19.9"* ]]
}

@test "a module never tagged here whose path the proxy serves is refused" {
  published github.com/kitsunium/sdk v0.19.7
  published github.com/kitsunium/sdk/pkg/v1/data v0.3.0
  g tag v0.19.7
  run floor
  [ "$status" -eq 1 ]
  [[ "$output" == *"pkg/v1/data has no tag in this clone, and the proxy lists v0.3.0"* ]]
}

@test "the tags of a manifest are checked, tombstones included" {
  published github.com/kitsunium/sdk/pkg v0.17.0 v0.18.0
  printf '{"modules":[{"tag":"pkg/v1/data/v0.1.0"}],"tombstones":["pkg/v0.18.0"]}' >"$BATS_TEST_TMPDIR/m.json"
  run floor --manifest="$BATS_TEST_TMPDIR/m.json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"ok pkg/v1/data/v0.1.0"* ]]
  [[ "$output" == *"pkg/v0.18.0 is at or below v0.18.0"* ]]
}

# A proxy that cannot answer is never "never published": that reading is the
# one that lets a v0.1.0 through over a published v0.19.7.
@test "a proxy that cannot be read is exit 2, never 'never published'" {
  run bash "$FLOOR" --proxy=http://127.0.0.1:9 v0.1.0
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not be reached"* ]]
}

@test "a tag no release cuts is refused" {
  run floor pin/sdk-api-0eb4ef16
  [ "$status" -eq 1 ]
  [[ "$output" == *"is no tag a release cuts"* ]]
}

# ── the rehearsal ───────────────────────────────────────────────────────────

@test "the rehearsal: pinned to v0.19.7, adding a family builds; its control is an ambiguous import" {
  if ! command -v go >/dev/null 2>&1; then skip "go absent"; fi
  run bash "$REHEARSAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"the root lifted to v0.20.0"* ]]
  [[ "$output" == *"fails with an ambiguous import, as it must"* ]]
}

@test "the rehearsal of a manifest with no family module is exit 3, never a pass" {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then skip "go/jq absent"; fi
  printf '{"modules":[{"dir":".","module":"github.com/kitsunium/sdk","version":"v0.19.8"}]}' >"$BATS_TEST_TMPDIR/m.json"
  run bash "$REHEARSAL" --manifest="$BATS_TEST_TMPDIR/m.json" --ref=HEAD
  [ "$status" -eq 3 ]
  [[ "$output" == *"nothing of ADR 0171's shape to rehearse"* ]]
}

# I5 (d): the rehearsal forces GOTOOLCHAIN=local, so a go older than the
# modules' go directive would fail every consumer obscurely; it says so first.
@test "the rehearsal refuses a go older than the repository's go directive, naming both" {
  if ! command -v jq >/dev/null 2>&1; then skip "jq absent"; fi
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  printf '#!/bin/sh\nif [ "$1 $2" = "env GOVERSION" ]; then echo go1.21.0; exit 0; fi\nexit 1\n' >"$BATS_TEST_TMPDIR/bin/go"
  chmod +x "$BATS_TEST_TMPDIR/bin/go"
  printf '{"root_floor":"v0.20.0","modules":[{"dir":"pkg/v1/data","module":"github.com/kitsunium/sdk/pkg/v1/data","version":"v0.1.0"}]}' >"$BATS_TEST_TMPDIR/m.json"
  run env PATH="$BATS_TEST_TMPDIR/bin:$PATH" bash "$REHEARSAL" --manifest="$BATS_TEST_TMPDIR/m.json" --ref=HEAD
  [ "$status" -eq 64 ]
  [[ "$output" == *"go 1.21.0 is older than go "*"put go "*"or newer on PATH"* ]]
}
