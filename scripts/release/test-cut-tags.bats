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
# g — every fixture git command, with hooks disabled at the ONE precedence that
# wins. `--no-verify` (kept below) only skips the VERIFICATION hooks: pre-commit
# and commit-msg. It does nothing about post-commit, post-merge or
# post-checkout, and an inherited core.hooksPath still runs those inside the
# disposable repository, where they can mutate state the later assertions read.
# Measured with a host post-commit that touches a marker file:
#
#   git commit --no-verify                       -> POST-COMMIT HOTE A TOURNE
#   git -c core.hooksPath=<empty> commit --no-verify -> propre
#
# A command-line `-c` outranks the global file AND an injected GIT_CONFIG_KEY_*,
# which a repo-local `git config` does not — all three were tried.
g() { git -c core.hooksPath="$NOHOOKS" "$@"; }

setup() {
  NOHOOKS="$BATS_TEST_TMPDIR/nohooks"
  mkdir -p "$NOHOOKS"
  REPO="$(mktemp -d)"
  cd "$REPO"
  g init -q -b main
  g config user.email "ci@example.invalid"
  g config user.name "ci"

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
  g add -A
  g commit -q --no-verify -m "init"

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
  rel="$(g commit-tree "HEAD^{tree}" -p "$(g rev-parse HEAD)" -m "release $1")"
  g tag "$1" "$rel"
}

# commit_pkg <message> — a commit that touches pkg/, so a Release-bump trailer
# in <message> is in scope for it.
commit_pkg() {
  echo "// $RANDOM" >>pkg/v1/codec.go
  g commit -aq --no-verify -F - <<<"$1"
}

# commit_other <message> — a commit that touches nothing under pkg/. A trailer
# here must NOT size the release (ADR 0007 §2).
commit_other() {
  mkdir -p docs
  echo "$RANDOM" >>docs/notes.md
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

# merge_branch <branch> <message> — land <branch> on main as a TRUE merge
# commit. Squash merges are the common flow, but 75 of main's last 500 commits
# are real merges, and they behave differently on both counts the trailer
# depends on: what the walk reaches, and what --name-only reports.
merge_branch() {
  g merge --no-ff --no-edit --no-verify -m "$2" "$1" >/dev/null
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
  g checkout -q -b side
  commit_pkg $'feat(codec): contributor work\n\nRelease-bump: minor'
  g checkout -q main
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
  g checkout -q -b side
  commit_pkg 'feat(codec): work, unsigned on the branch'
  g checkout -q main
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
  g checkout -q -b side
  commit_other 'docs: branch work'
  g checkout -q main
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

# The per-commit path check was `git log --name-only … | grep -qE '^pkg/'` with
# `|| continue` behind it. `grep -q` exits on its first match, git takes SIGPIPE,
# `pipefail` reports 141, and `|| continue` SKIPS the commit — so a merge that
# touched pkg/ AND enough other paths to fill the pipe buffer had its trailer
# discarded and the release fell back to a patch. Measured on a commit touching
# pkg/ plus ~300 KB of other names: rc=141, first `pkg/` line at position 1.
#
# The fixture uses long names rather than many files so it costs milliseconds:
# 1600 paths of ~210 bytes is ~340 KB. Measured on this shape: 400 paths (85 KB)
# fails the pipeline 9 times in 10 — flaky, so the fixture is sized to the
# regime where it fails 10 in 10.
@test "a trailer survives a commit whose file list exceeds the pipe buffer" {
  need_toolchain
  tag_release pkg/v0.1.0
  mkdir -p tools
  pad="$(printf 'y%.0s' $(seq 1 200))"
  for i in $(seq 1 1600); do : >"tools/${pad}${i}.go"; done
  echo "// touched" >>pkg/v1/codec.go
  g add -A
  g commit -q --no-verify -F - <<<$'feat(codec): wide merge\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

@test "a commit repeating the same trailer still means that trailer" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): said twice\n\nRelease-bump: minor\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# ---------------------------------------------------------------------------
# The FORM of the message, not its values.
#
# Every one of the twenty cases above builds its message as
# $'subject\n\nRelease-bump: minor' — the trailer always in the LAST paragraph.
# Twenty scenarios, unanimous, and blind to the only disposition that has ever
# cost a release: `git interpret-trailers` and `%(trailers:…)` parse the LAST
# PARAGRAPH only, so a trailer followed by anything else is invisible.
#
# It already happened. Release v0.3.4, range 13a33b6b..e435af8c: e435af8c (#207)
# carried `Release-bump: minor` at column 0 AND touched three files under pkg/,
# but the trailer sat at line 106 of a 138-line message that GitHub composed
# from the branch commits, followed by bullet sections and their prose bodies.
# The parser returned empty, next_patch won, and pkg/v0.3.4 shipped. pkg/v0.4.0
# has never existed. Nothing failed and nothing warned.
#
# Measured on git 2.47.3, three dispositions of the last paragraph:
#
#   Refs: #127  + Release-bump: patch   -> patch
#   Refs #127   + Release-bump: patch   -> EMPTY   (no colon, block not a block)
#   Release-bump: patch alone           -> patch
#
# The decision: refuse loudly rather than read the trailer wherever it appears.
# Reading it anywhere would let a CONTRIBUTOR set the release size, because the
# squash message GitHub composes embeds the branch commits' own bodies — that is
# exactly the smuggling ADR 0007 §2 exists to prevent. A refusal cannot set a
# size; it can only stop a release, visibly, with the commit named.

@test "a Release-bump outside the trailer block is refused, not ignored" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): fifty public symbols\n\nRelease-bump: minor\n\n* fix(vcs): a branch commit GitHub folded in\n\nAnd the prose body that came with it.'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -ne 0 ]
  [[ "$output" == *"OUTSIDE the trailer block"* ]]
  [[ "$output" != *"would tag chain"* ]]
}

# The #207 shape, reproduced: trailer at column 0, then a bullet section with a
# prose body under it. This is the message that shipped a minor as a patch.
@test "the shape that shipped pkg/v0.3.4 instead of pkg/v0.4.0 is refused" {
  need_toolchain
  tag_release pkg/v0.3.3
  commit_pkg $'fix(vcs): six of ADR 0076\'s seven deferred entries\n\nADR 0087 records all of it.\n\nRelease-bump: minor\n\n* fix(vcs): the child prefix of a filesystem root is not root plus a separator\n\nQodo found it on #207 and it is real: spelledAs built its prefix as\n`s.repoRoot + separator`.'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -ne 0 ]
  [[ "$output" == *"OUTSIDE the trailer block"* ]]
}

# `Refs` WITHOUT a colon is not a trailer, so git stops treating the block as a
# trailer block and takes the Release-bump down with it. Same paragraph, same
# adjacency, one missing character.
@test "a malformed neighbour in the trailer block is refused, not ignored" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): symbols\n\nRefs #127\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -ne 0 ]
  [[ "$output" == *"OUTSIDE the trailer block"* ]]
}

# …and the well-formed neighbour still works, so the refusal is about the block
# being broken and not about having neighbours at all.
@test "a well-formed neighbour in the trailer block still sizes the release" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): symbols\n\nRefs: #127\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# A commit that never mentions a bump is not suspicious, at any shape. The
# refusal must fire on a bump the parser MISSED, never on its absence.
@test "a multi-paragraph message with no bump at all is still a patch" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'fix(codec): one\n\n* fix: a folded branch commit\n\nProse body.\n\n* fix: another'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# ── ADR 0089: one notion of "a file that counts", shared with compute-bumps ───
#
# These run together on purpose. Each one alone passes under a rule that is
# wrong in the other direction, so a suite carrying only one of them would
# certify the defect it does not test.

# commit_internal <message> — touches internal/ and NOTHING under pkg/. Before
# ADR 0089 a `^pkg/` scope skipped its trailer, so a behavioural change that
# ADR 0007 §2 row 3 REQUIRES to be a minor could only ship as a patch — while
# compute-bumps.sh cut the release for it anyway, through rdeps.
commit_internal() {
  echo "// $RANDOM" >>internal/service/svc.go
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

# commit_pkg_doc <message> — touches ONLY a CLAUDE.md under pkg/.
# compute-bumps.sh:70 refuses to cut a release for that churn; before ADR 0089
# cut-tags.sh still let it SIZE one, which was the cheapest way past the scope.
commit_pkg_doc() {
  mkdir -p pkg/v1/foo
  echo "# $RANDOM" >>pkg/v1/foo/CLAUDE.md
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

@test "a trailer on an internal/-only commit sizes the release (ADR 0089)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_internal $'feat(service): behaviour observable through pkg\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

@test "a trailer on a pkg/ CLAUDE.md alone does NOT size the release (ADR 0089)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg_doc $'docs: one line under pkg/\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" != *"pkg/v0.2.0"* ]]
}

# The control. A rule answering "no" to everything would pass both tests above;
# this is what stops that.
@test "a trailer on pkg/ code still sizes the release (ADR 0089 control)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): public symbols\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# The refusal inherits the same scope. A buried trailer on a commit that cannot
# cut a release is still an alarm about nothing; one on an internal/-only commit
# is not, because that commit can now size a release — so a trailer missed there
# is exactly the silent patch the refusal exists to stop.
@test "a buried trailer on an internal/-only commit is refused (ADR 0089)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_internal $'feat(service): behaviour\n\nRelease-bump: minor\n\nProse after the trailer hides it.'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -ne 0 ]
  [[ "$output" == *"OUTSIDE the trailer block"* ]]
}

# commit_pkg_bench <message> — touches ONLY a BENCH.md under pkg/. ADR 0089
# settled that a file which cannot CUT a release must not SIZE one, and then
# spelled "which files" as two names in two places. A BENCH.md is maintainer-only
# by that ADR's own definition and was in neither list, so it could both cut a
# release (it cut pkg/v0.4.4 — #238) and size one. It can now do neither, and
# the two halves read the one predicate in lib/release-scope.sh.
commit_pkg_bench() {
  mkdir -p pkg/v1/foo
  printf 'BenchmarkFoo-8  %s ns/op\n' "$RANDOM" >>pkg/v1/foo/BENCH.md
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

@test "a trailer on a pkg/ BENCH.md alone does NOT size the release (#238)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg_bench $'docs(bench): re-measure\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" != *"pkg/v0.2.0"* ]]
}

# And the refusal follows the same scope, for the same reason the CLAUDE.md row
# above does: a buried trailer on a commit that cannot size a release is an
# alarm about nothing. Without this the widened category would have turned every
# BENCH.md merge carrying a long squash message into a red release.
@test "a buried trailer on a pkg/ BENCH.md alone is not an alarm (#238)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg_bench $'docs(bench): re-measure\n\nRelease-bump: minor\n\nProse after the trailer hides it.'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" != *"OUTSIDE the trailer block"* ]]
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
