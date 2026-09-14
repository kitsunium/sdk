#!/usr/bin/env bats
# BATS tests for cut-tags.sh. Stays in dry-run / pre-push so no real tag is
# ever pushed. Covers: the publishable chain rewrite (drop replace + pin
# intra-repo deps + chain tags), the bootstrap auto-cut guard (ADR 0009), how
# the Release-bump trailer is read across the release range (ADR 0085), and the
# tag-format library (canonical + internal tag shapes, bumps, sort).

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
  git config user.name "ci"

  # Full publish chain with the real kitsunium module paths — cut-tags pins
  # `github.com/kitsunium/sdk/internal/*` requires, so the fixture must use
  # those paths for the rewrite to act. The public module is the bare `pkg`
  # (go.mod at pkg/go.mod, module …/pkg); consumer code lives under pkg/v1/.
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

  cat >pkg/go.mod <<'EOF'
module github.com/kitsunium/sdk/pkg

go 1.26

require (
	github.com/kitsunium/sdk/internal/core v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/kernel v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/service v0.0.0-00010101000000-000000000000
)

replace (
	github.com/kitsunium/sdk/internal/core => ../internal/core
	github.com/kitsunium/sdk/internal/kernel => ../internal/kernel
	github.com/kitsunium/sdk/internal/service => ../internal/service
)
EOF

  : >pkg/v1/codec.go
  git add -A
  git commit -q --no-verify -m "init"

  SCRIPT="$BATS_TEST_DIRNAME/cut-tags.sh"
  LIB="$BATS_TEST_DIRNAME/lib/tag-format.sh"
}

teardown() { rm -rf "$REPO"; }

# tag_release <tag> — tag a DETACHED child of HEAD, exactly how cut-tags.sh
# publishes a release (ADR 0009). Both halves of the release baseline on the
# tag's FIRST PARENT, so a fixture that tags HEAD directly would exercise a
# range that never occurs in production.
tag_release() {
  local rel
  rel="$(git commit-tree "HEAD^{tree}" -p "$(git rev-parse HEAD)" -m "release $1")"
  git tag "$1" "$rel"
}

# commit_pkg <message> — a commit that touches pkg/, so a Release-bump trailer
# in <message> is in scope for it.
commit_pkg() {
  echo "// $RANDOM" >>pkg/v1/codec.go
  git commit -aq --no-verify -F - <<<"$1"
}

# commit_other <message> — a commit that touches nothing under pkg/. A trailer
# here must NOT size the release (ADR 0007 §2).
commit_other() {
  mkdir -p docs
  echo "$RANDOM" >>docs/notes.md
  git add -A
  git commit -q --no-verify -F - <<<"$1"
}

# merge_branch <branch> <message> — land <branch> on main as a TRUE merge
# commit. Squash merges are the common flow, but 75 of main's last 500 commits
# are real merges, and they behave differently on both counts the trailer
# depends on: what the walk reaches, and what --name-only reports.
merge_branch() {
  git merge --no-ff --no-edit --no-verify -m "$2" "$1" >/dev/null
}

# The dry-run rewrites every chain go.mod, so it needs the Go + jq toolchain.
need_toolchain() {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then skip "go/jq absent"; fi
}

@test "bootstrap dry-run prints the publishable chain (replace dropped, deps pinned, chain tags)" {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then skip "go/jq absent"; fi
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"would tag chain: internal/kernel/v0.1.0 internal/core/v0.1.0 internal/service/v0.1.0 pkg/v0.1.0"* ]]
  # pkg pins its intra-repo deps to the release version …
  [[ "$output" == *"internal/service v0.1.0"* ]]
  # … and the local replace is gone.
  [[ "$output" != *"=> ../"* ]]
}

@test "bootstrap auto-cut is refused without --allow-bootstrap (ADR 0009, exit 3)" {
  run bash -c "echo pkg | $SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" == *"refusing to auto-cut the FIRST release"* ]]
}

@test "legacy 'vN' bump token still maps to the single public module" {
  run bash -c "echo v1 | $SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" == *"refusing to auto-cut the FIRST release (pkg/v0.1.0)"* ]]
}

# Regression (ADR 0085): a release only fires on a SUCCESSFUL CI run, and a run
# cancelled by the next push produces none — so the commit carrying the trailer
# routinely is not HEAD when the release finally runs. compute-bumps.sh still
# measures its pkg/ change over the range, so reading the trailer from HEAD
# alone shipped a minor's worth of API as a patch.
@test "a trailer behind HEAD still sizes the release (ADR 0085)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'refactor: fifty new public symbols\n\nRelease-bump: minor'
  commit_other 'docs: a second merge twelve seconds later'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"would tag chain: internal/kernel/v0.2.0 internal/core/v0.2.0 internal/service/v0.2.0 pkg/v0.2.0"* ]]
  # …and it says where it got the trailer, since HEAD no longer shows it.
  [[ "$output" == *"not HEAD"* ]]
}

# The per-commit scoping is the whole reason the trailer cannot be smuggled in:
# it counts only for the paths its own commit touched. A range-wide read would
# let this docs merge's trailer ride on the other commit's pkg/ change.
@test "a trailer on a commit that touched no pkg/ still does not count" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): a real pkg change, unsigned'
  commit_other $'docs: unrelated\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# Largest wins: a later commit that says nothing cannot shrink one that did.
@test "the largest trailer in the range wins" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): a\n\nRelease-bump: minor'
  commit_pkg $'feat(codec): b\n\nRelease-bump: major'
  commit_other 'docs: tail'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  # v0 -> v1 is the stabilisation step on the same bare module path (ADR 0009).
  [[ "$output" == *"pkg/v1.0.0"* ]]
}

# The range opens AFTER the commit the last release was cut from, so a trailer
# the previous release already honoured cannot be honoured a second time.
@test "a trailer already consumed by the previous release is not applied twice" {
  need_toolchain
  commit_pkg $'feat(codec): symbols\n\nRelease-bump: minor'
  tag_release pkg/v0.2.0
  commit_pkg 'fix(codec): follow-up'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.1"* ]]
}

@test "several commits, none carrying a trailer, is still a patch" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): one'
  commit_pkg 'fix(codec): two'
  commit_other 'docs: three'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" != *"not HEAD"* ]]
}

@test "the trailer on HEAD keeps working, and says nothing about provenance" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): signed on the last merge\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
  [[ "$output" != *"not HEAD"* ]]
}

# 'Release-bump: major' from a v1 base is rejected BY NAME (ADR 0009): a
# breaking v2 needs a real …/pkg/v2 module path. Reading only HEAD made that
# refusal unreachable whenever the trailer sat behind HEAD — it silently cut a
# patch instead.
@test "'Release-bump: major' from a v1 base is refused even from behind HEAD" {
  need_toolchain
  tag_release pkg/v1.2.3
  commit_pkg $'feat(codec): breaking\n\nRelease-bump: major'
  commit_other 'docs: tail'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -ne 0 ]
  [[ "$output" == *"needs a real"* ]]
  [[ "$output" != *"would tag chain"* ]]
}

# ADR 0007 §2 gates a minor on a trailer "an attacker cannot smuggle through a
# PR body" by reading it from the merge commit — the one message a maintainer
# writes. A range walk that descends into the commits a merge brought IN would
# honour the contributor's own trailer instead. Five commits on this repository
# carry the trailer, touch pkg/, and are not on main's first-parent line.
@test "a trailer on a commit a merge brought in does not count" {
  need_toolchain
  tag_release pkg/v0.1.0
  git checkout -q -b side
  commit_pkg $'feat(codec): contributor work\n\nRelease-bump: minor'
  git checkout -q main
  merge_branch side 'Merge the contributor branch'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# The other half of the same fact: a plain --name-only reports NO files for a
# merge commit, so a maintainer's trailer on one was scoped against an empty
# list and counted for nothing — 14 of main's 33 trailer-bearing commits are
# merges. The path check asks for the diff against parent 1 instead.
@test "a trailer on a merge commit is scoped by what the merge brought in" {
  need_toolchain
  tag_release pkg/v0.1.0
  git checkout -q -b side
  commit_pkg 'feat(codec): work, unsigned on the branch'
  git checkout -q main
  merge_branch side $'Merge the branch\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# …and that scoping still bites: a merge that brought in nothing under pkg/
# cannot be sized by its own trailer either.
@test "a trailer on a merge that brought in no pkg/ still does not count" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): a real pkg change on main, unsigned'
  git checkout -q -b side
  commit_other 'docs: branch work'
  git checkout -q main
  merge_branch side $'Merge the docs branch\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# git parses the trailer OPTION LIST on commas, so `separator=,` left the
# separator empty and two repeated trailers were concatenated into one field:
# `Release-bump: mi` + `Release-bump: nor` produced the single value `minor` and
# cut a minor release. Measured on git 2.47.3; the separator is %x1F.
@test "two repeated trailers do not concatenate into a third value" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): halves that spell a bump\n\nRelease-bump: mi\nRelease-bump: nor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

@test "a commit repeating the same trailer still means that trailer" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): said twice\n\nRelease-bump: minor\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

@test "an unwalkable --range is refused rather than read as no trailer" {
  run bash -c "echo pkg | $SCRIPT --dry-run --range=nosuchrev..HEAD"
  [ "$status" -eq 64 ]
  [[ "$output" == *"cannot walk the release range"* ]]
}

@test "lib: is_valid_tag accepts the bare pkg shape (major 0|1)" {
  source "$LIB"
  run is_valid_tag "pkg/v0.1.0"
  [ "$status" -eq 0 ]
  run is_valid_tag "pkg/v1.0.0-rc.1"
  [ "$status" -eq 0 ]
}

@test "lib: is_valid_tag rejects v2+ major and the legacy 3-component shape" {
  source "$LIB"
  # Bare module path => major must be 0 or 1; v2+ needs a /v2 module path.
  run is_valid_tag "pkg/v2.0.0"
  [ "$status" -ne 0 ]
  # The old pkg/<major>/vX.Y.Z shape is no longer a valid pkg tag.
  run is_valid_tag "pkg/v1/v1.0.0"
  [ "$status" -ne 0 ]
}

@test "lib: is_valid_tag rejects shell-injection bait" {
  source "$LIB"
  run is_valid_tag 'pkg/v0.1.0;rm -rf /'
  [ "$status" -ne 0 ]
  run is_valid_tag 'pkg/v0.1.0 --upload-pack=/evil'
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
  run is_valid_internal_tag "pkg/v0.1.0"
  [ "$status" -ne 0 ]
}

@test "lib: next_patch increments PATCH only" {
  source "$LIB"
  run next_patch "pkg/v0.1.0"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg/v0.1.1" ]
}

@test "lib: next_patch refuses pre-release base" {
  source "$LIB"
  run next_patch "pkg/v0.1.0-rc.1"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-release"* ]]
}

@test "lib: next_minor resets PATCH to 0" {
  source "$LIB"
  run next_minor "pkg/v0.1.5"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg/v0.2.0" ]
}

@test "lib: version_sort orders patches correctly cross-platform" {
  source "$LIB"
  run bash -c 'printf "pkg/v0.1.10\npkg/v0.1.2\npkg/v0.1.9\n" | { source "'"$LIB"'"; version_sort; } | tail -n1'
  [ "$output" = "pkg/v0.1.10" ]
}
