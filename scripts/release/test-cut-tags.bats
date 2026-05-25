#!/usr/bin/env bats
# BATS tests for cut-tags.sh. Stays in dry-run / pre-push so no real tag is
# ever pushed. Covers: the publishable chain rewrite (drop replace + pin
# intra-repo deps + chain tags), the bootstrap auto-cut guard (ADR 0009), and
# the tag-format library (canonical + internal tag shapes, bumps, sort).

setup() {
  REPO="$(mktemp -d)"
  cd "$REPO"
  git init -q -b main
  git config user.email "ci@example.invalid"
  git config user.name "ci"

  # Full publish chain with the real kitsunium module paths — cut-tags pins
  # `github.com/kitsunium/sdk/internal/*` requires, so the fixture must use
  # those paths for the rewrite to act.
  mkdir -p internal/kernel internal/core internal/service pkg/v1

  cat >internal/kernel/go.mod <<'EOF'
module github.com/kitsunium/sdk/internal/kernel

go 1.26
EOF

  cat >internal/core/go.mod <<'EOF'
module github.com/kitsunium/sdk/internal/core

go 1.26

require github.com/kitsunium/sdk/internal/kernel v0.0.0-00010101000000-000000000000

replace github.com/kitsunium/sdk/internal/kernel => ../kernel
EOF

  cat >internal/service/go.mod <<'EOF'
module github.com/kitsunium/sdk/internal/service

go 1.26

require (
	github.com/kitsunium/sdk/internal/core v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/kernel v0.0.0-00010101000000-000000000000
)

replace (
	github.com/kitsunium/sdk/internal/core => ../core
	github.com/kitsunium/sdk/internal/kernel => ../kernel
)
EOF

  cat >pkg/v1/go.mod <<'EOF'
module github.com/kitsunium/sdk/pkg/v1

go 1.26

require (
	github.com/kitsunium/sdk/internal/core v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/kernel v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/service v0.0.0-00010101000000-000000000000
)

replace (
	github.com/kitsunium/sdk/internal/core => ../../internal/core
	github.com/kitsunium/sdk/internal/kernel => ../../internal/kernel
	github.com/kitsunium/sdk/internal/service => ../../internal/service
)
EOF

  : >pkg/v1/codec.go
  git add -A
  git commit -q -m "init"

  SCRIPT="$BATS_TEST_DIRNAME/cut-tags.sh"
  LIB="$BATS_TEST_DIRNAME/lib/tag-format.sh"
}

teardown() { rm -rf "$REPO"; }

@test "bootstrap dry-run prints the publishable chain (replace dropped, deps pinned, chain tags)" {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then skip "go/jq absent"; fi
  run bash -c "echo v1 | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"would tag chain: internal/kernel/v1.0.0 internal/core/v1.0.0 internal/service/v1.0.0 pkg/v1/v1.0.0"* ]]
  # pkg/v1 pins its intra-repo deps to the release version …
  [[ "$output" == *"internal/service v1.0.0"* ]]
  # … and the local replace is gone.
  [[ "$output" != *"=> ../"* ]]
}

@test "bootstrap auto-cut is refused without --allow-bootstrap (ADR 0009, exit 3)" {
  run bash -c "echo v1 | $SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" == *"refusing to auto-cut the FIRST release"* ]]
}

@test "lib: is_valid_tag accepts canonical shape" {
  source "$LIB"
  run is_valid_tag "pkg/v1/v1.32.1"
  [ "$status" -eq 0 ]
  run is_valid_tag "pkg/v2/v2.0.0-rc.1"
  [ "$status" -eq 0 ]
}

@test "lib: is_valid_tag rejects path-major != semver-major" {
  source "$LIB"
  run is_valid_tag "pkg/v1/v2.0.0"
  [ "$status" -ne 0 ]
  run is_valid_tag "pkg/v2/v1.2.3"
  [ "$status" -ne 0 ]
}

@test "lib: is_valid_tag rejects shell-injection bait" {
  source "$LIB"
  run is_valid_tag 'pkg/v1/v1.0.0;rm -rf /'
  [ "$status" -ne 0 ]
  run is_valid_tag 'pkg/v1/v1.0.0 --upload-pack=/evil'
  [ "$status" -ne 0 ]
  run is_valid_tag '../etc/passwd'
  [ "$status" -ne 0 ]
}

@test "lib: is_valid_internal_tag accepts internal/<mod>/vX.Y.Z (major 0|1 only)" {
  source "$LIB"
  run is_valid_internal_tag "internal/core/v1.0.0"
  [ "$status" -eq 0 ]
  run is_valid_internal_tag "internal/kernel/v0.3.1"
  [ "$status" -eq 0 ]
  # v2+ needs /vN module paths — rejected (deferred per ADR 0009).
  run is_valid_internal_tag "internal/core/v2.0.0"
  [ "$status" -ne 0 ]
  run is_valid_internal_tag "pkg/v1/v1.0.0"
  [ "$status" -ne 0 ]
}

@test "lib: next_patch increments PATCH only" {
  source "$LIB"
  run next_patch "pkg/v1/v1.32.0"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg/v1/v1.32.1" ]
}

@test "lib: next_patch refuses pre-release base" {
  source "$LIB"
  run next_patch "pkg/v1/v1.0.0-rc.1"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-release"* ]]
}

@test "lib: next_minor resets PATCH to 0" {
  source "$LIB"
  run next_minor "pkg/v1/v1.32.5"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg/v1/v1.33.0" ]
}

@test "lib: major_from_tag extracts vN" {
  source "$LIB"
  run major_from_tag "pkg/v3/v3.0.0"
  [ "$status" -eq 0 ]
  [ "$output" = "v3" ]
}

@test "lib: version_sort orders patches correctly cross-platform" {
  source "$LIB"
  run bash -c 'printf "pkg/v1/v1.0.10\npkg/v1/v1.0.2\npkg/v1/v1.0.9\n" | { source "'"$LIB"'"; version_sort; } | tail -n1'
  [ "$output" = "pkg/v1/v1.0.10" ]
}
