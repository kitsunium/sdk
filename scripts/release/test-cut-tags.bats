#!/usr/bin/env bats
# BATS tests for cut-tags.sh. Stays in dry-run / pre-push so no real tag is
# ever pushed. Covers: the publishable chain rewrite (drop replace + pin
# intra-repo deps + chain tags), the bootstrap auto-cut guard (ADR 0009), how
# the size is read from the merged pull requests' labels across the release
# range (ADR 0085, ADR 0135) — through a gh stub, see test-helpers.bash — and
# the tag-format and release-size libraries.

load test-helpers

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
  SIZE_LIB="$BATS_TEST_DIRNAME/lib/release-size.sh"
  install_gh_stub
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

# commit_pkg <message> — a commit that touches pkg/, so the size its pull
# request carries — or the request its <message> makes — is in scope for it.
commit_pkg() {
  echo "// $RANDOM" >>pkg/v1/codec.go
  g commit -aq --no-verify -F - <<<"$1"
}

# commit_other <message> — a commit that touches nothing under pkg/ or
# internal/. Nothing about it may size the release (ADR 0089).
commit_other() {
  mkdir -p docs
  echo "$RANDOM" >>docs/notes.md
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

# merge_branch <branch> <message> — land <branch> on main as a TRUE merge
# commit. Squash merges are the common flow, but 75 of main's last 500 commits
# are real merges, and they behave differently on both counts the size depends
# on: what the walk reaches, and what --name-only reports.
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

# ── ADR 0135: a release is sized by a maintainer's label on the merged PR ────
#
# The size used to be a `Release-bump:` trailer in the first-parent commit
# message, and this repository squash-merges with COMMIT_MESSAGES, so that
# message is composed from the branch commits — contributor text. It buried
# maintainer trailers (pkg/v0.1.35 and pkg/v0.3.4 shipped as patches, #217), it
# let a contributor set the size (#224), and ADR 0089's refusal, which stopped
# the first, blocked the automatic release of ce9323c until the tags were cut by
# hand (#248's squash carried a branch commit's trailer mid-body). A label can
# only be set by an account with triage or write access, and it has no paragraph
# rule to break. What the message still does is ASK: a request for more than a
# patch with no label to decide it is refused before anything is published,
# because giving the patch would lose a request somebody may have meant.
#
# Everything below still holds from ADR 0085 and ADR 0089 — the range, the
# first-parent walk, the per-commit scope — with the label in the trailer's
# place. The rows that asserted the trailer PARSER (the %x1F separator, the last
# paragraph, the malformed neighbour) are gone with the parser: the size is no
# longer parsed out of text at all, and the rows that replace them assert that a
# label decides whatever the text looks like.

# Regression (ADR 0085): a release only fires on a SUCCESSFUL CI run, and a run
# cancelled by the next push produces none — so the merge that was sized
# routinely is not HEAD when the release finally runs. compute-bumps.sh still
# measures its pkg/ change over the range, so reading HEAD alone shipped a
# minor's worth of API as a patch.
@test "a label behind HEAD still sizes the release (ADR 0085)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'refactor: fifty new public symbols'
  label_pr 101 release:minor
  commit_other 'docs: a second merge twelve seconds later'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"would tag chain: internal/kernel/v0.2.0 internal/core/v0.2.0 internal/service/v0.2.0 pkg/v0.2.0"* ]]
  # …and it says which merge sized it, since HEAD no longer shows it.
  [[ "$output" == *"label release:minor on #101"* ]]
  [[ "$output" == *"not HEAD"* ]]
}

# The per-commit scoping (ADR 0089): a label counts only for a merge that could
# itself cut a release. A range-wide read would let this docs merge's label ride
# on the other commit's pkg/ change. The docs merge is not even looked up — the
# paths are read from git first — so exactly one lookup happens.
@test "a label on a merge that touched nothing releasable does not count" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): a real pkg change, unlabelled'
  commit_other 'docs: unrelated'
  label_pr 102 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [ "$(gh_calls)" -eq 1 ]
}

# Largest wins: a later merge that says nothing cannot shrink one that did.
@test "the largest label in the range wins" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): a'
  label_pr 103 release:minor
  commit_pkg 'feat(codec): b'
  label_pr 104 release:major
  commit_other 'docs: tail'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  # v0 -> v1 is the stabilisation step on the same bare module path (ADR 0009).
  [[ "$output" == *"pkg/v1.0.0"* ]]
}

# The range opens AFTER the commit the last release was cut from, so a label the
# previous release already honoured cannot be honoured a second time.
@test "a label already consumed by the previous release is not applied twice" {
  need_toolchain
  commit_pkg 'feat(codec): symbols'
  label_pr 105 release:minor
  tag_release pkg/v0.2.0
  commit_pkg 'fix(codec): follow-up'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.1"* ]]
}

@test "several merges, none labelled and none asking, is still a patch" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): one'
  commit_pkg 'fix(codec): two'
  commit_other 'docs: three'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" == *"no merge in the range is labelled above patch"* ]]
  [[ "$output" != *"not HEAD"* ]]
}

@test "a label on HEAD sizes the release, and says nothing about provenance" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): labelled on the last merge'
  label_pr 106 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
  [[ "$output" != *"not HEAD"* ]]
}

# A major from a v1 base is refused BY NAME (ADR 0009): a breaking v2 needs a
# real …/pkg/v2 module path. It must stay refused when the label sits behind
# HEAD, which is where reading HEAD alone used to make the refusal unreachable.
@test "a major label from a v1 base is refused even from behind HEAD" {
  need_toolchain
  tag_release pkg/v1.2.3
  commit_pkg 'feat(codec): breaking'
  label_pr 107 release:major
  commit_other 'docs: tail'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -ne 0 ]
  [[ "$output" == *"needs a real"* ]]
  [[ "$output" != *"would tag chain"* ]]
}

# The first-parent walk: the commits a TRUE merge brought in are the
# contributor's own, and nothing about them sizes a release. Here the branch
# commit even has a labelled pull request of its own in the fixture — the walk
# must never ask about it.
@test "a commit a merge brought in is never looked up" {
  need_toolchain
  tag_release pkg/v0.1.0
  g checkout -q -b side
  commit_pkg $'feat(codec): contributor work\n\nRelease-bump: minor'
  label_pr 108 release:minor
  side_sha="$(g rev-parse HEAD)"
  g checkout -q main
  merge_branch side 'Merge the contributor branch'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  run grep -c "$side_sha" "$GH_FIXTURES/calls.log"
  [ "$output" = "0" ]
}

# A plain --name-only reports NO files for a merge commit, so the merge's own
# pull request used to be scoped against an empty list and count for nothing.
# The path check asks for the diff against parent 1 instead.
@test "a label on a merge commit is scoped by what the merge brought in" {
  need_toolchain
  tag_release pkg/v0.1.0
  g checkout -q -b side
  commit_pkg 'feat(codec): work, unlabelled on the branch'
  g checkout -q main
  merge_branch side 'Merge pull request #109'
  label_pr 109 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# …and that scoping still bites: a merge that brought in nothing under pkg/ or
# internal/ cannot be sized by its own label either.
@test "a label on a merge that brought in nothing releasable does not count" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): a real pkg change on main, unlabelled'
  g checkout -q -b side
  commit_other 'docs: branch work'
  g checkout -q main
  merge_branch side 'Merge pull request #110'
  label_pr 110 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# The per-commit path check was `git log --name-only … | grep -qE '^pkg/'` with
# `|| continue` behind it. `grep -q` exits on its first match, git takes SIGPIPE,
# `pipefail` reports 141, and `|| continue` SKIPS the commit — so a merge that
# touched pkg/ AND enough other paths to fill the pipe buffer lost its size. The
# fixture uses long names rather than many files so it costs milliseconds: 1600
# paths of ~210 bytes is ~340 KB, the regime where the old shape failed 10 in 10.
@test "a label survives a merge whose file list exceeds the pipe buffer" {
  need_toolchain
  tag_release pkg/v0.1.0
  mkdir -p tools
  pad="$(printf 'y%.0s' $(seq 1 200))"
  for i in $(seq 1 1600); do : >"tools/${pad}${i}.go"; done
  echo "// touched" >>pkg/v1/codec.go
  g add -A
  g commit -q --no-verify -m 'feat(codec): wide merge'
  label_pr 111 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# ── The message asks; only a label decides ──────────────────────────────────

# The decision itself. A well-formed trailer in the last paragraph — the exact
# shape that used to cut a minor — no longer sizes anything: nothing says a
# maintainer wrote it. It is not ignored either, which would lose the request
# (#217); it stops the release and names the ways out.
@test "a trailer alone no longer sizes a release: unlabelled, it is refused (ADR 0135)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): symbols\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"asks for 'Release-bump: minor'"* ]]
  [[ "$output" == *"no merged pull request introduced it"* ]]
  [[ "$output" == *"bump=<patch|minor|major>"* ]]
  [[ "$output" != *"would tag chain"* ]]
}

# #224, closed: a contributor's `Release-bump: minor` in the last paragraph was
# honoured exactly like a maintainer's. release:patch is how a maintainer
# declines it without editing anybody's commits.
@test "a contributor's request is declined by release:patch (#224)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): a contributor proposal\n\n* feat(codec): first branch commit\n\nContributor prose.\n\nRelease-bump: minor'
  label_pr 112 release:patch
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" == *"the label decides"* ]]
}

@test "a label sizes a merge whose message says nothing about size" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  label_pr 113 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# The #207 shape — trailer at column 0, then a folded branch commit with a prose
# body — is what shipped pkg/v0.3.4 instead of pkg/v0.4.0. Labelled, it cuts the
# minor it asked for: where the line sits no longer matters.
@test "the shape that shipped pkg/v0.3.4 instead of pkg/v0.4.0 now cuts the minor" {
  need_toolchain
  tag_release pkg/v0.3.3
  commit_pkg $'fix(vcs): six of ADR 0076\'s seven deferred entries\n\nADR 0087 records all of it.\n\nRelease-bump: minor\n\n* fix(vcs): the child prefix of a filesystem root is not root plus a separator\n\nQodo found it on #207 and it is real.'
  label_pr 207 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.4.0"* ]]
}

# The #248 shape: the squash message carried a branch commit's trailer mid-body,
# the job exited 65 and the tags were cut by hand. It is still refused — nothing
# decides it — but the refusal now names the pull request and the two
# maintainer actions that settle it, neither of which is a hand-run script.
@test "the #248 shape, unlabelled, is refused and names the way out" {
  need_toolchain
  tag_release pkg/v0.5.0
  commit_pkg $'feat: framework wave 2\n\n* feat(vcs): git.Head\n\nRelease-bump: minor\n\n* feat(redact): a secret shown is a secret replaced\n\nProse that followed.'
  label_pr 248
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"#248 carries no release label"* ]]
  [[ "$output" == *"label #248 release:minor"* ]]
  [[ "$output" == *"re-run this job"* ]]
  [[ "$output" == *"bump=<patch|minor|major>"* ]]
}

@test "two different release labels on one pull request are refused" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  label_pr 114 release:minor release:patch
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"two release labels"* ]]
}

# A typo'd release label that read as "no label" would be a minor published as a
# patch — the #217 outcome through a new door.
@test "a release label that names no size is refused" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  label_pr 115 release:minr
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"is not a release size"* ]]
}

# The control for the two above: labels that are not release labels are none of
# this script's business.
@test "labels that are not release labels size nothing" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'fix(codec): one'
  label_pr 116 bug documentation
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# A pull request that was closed without merging introduced nothing.
@test "an unmerged pull request's label does not size the release" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  sha="$(g rev-parse HEAD)"
  printf '[{"number":117,"merged_at":null,"labels":[{"name":"release:minor"}]}]\n' \
    >"$GH_FIXTURES/commits_${sha}_pulls.json"
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# The absence-of-measurement class (#226, #227) in its newest form: a lookup
# that failed must not read as a merge without a label, i.e. as a patch.
@test "a lookup GitHub could not answer is refused, never read as a patch" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  : >"$GH_FIXTURES/commits_$(g rev-parse HEAD)_pulls.fail"
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"could not be read"* ]]
  [[ "$output" == *"HTTP 502"* ]]
  [[ "$output" != *"would tag chain"* ]]
}

@test "a missing gh is refused, never read as a patch" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  RELEASE_GH="$BATS_TEST_TMPDIR/no-such-gh" run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"is not on PATH"* ]]
}

# Two values that are not sizes do not spell one. The old parser needed the
# %x1F separator to keep `mi` and `nor` apart; the line scan never joins lines.
@test "halves of a word in two lines do not spell a size" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): halves that spell a bump\n\nRelease-bump: mi\nRelease-bump: nor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# Asking for the default is not asking for anything a label must grant.
@test "a message asking for a patch needs no label" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'fix(codec): one\n\nRelease-bump: patch'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# A commit that never mentions a size is not suspicious, at any shape.
@test "a multi-paragraph message with no request and no label is a patch" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'fix(codec): one\n\n* fix: a folded branch commit\n\nProse body.\n\n* fix: another'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

# ── --bump: the maintainer states the size, and GitHub is not asked ─────────

# The recovery path #224 asked for: a refused release is settled by dispatching
# SDK Release with `bump`, which passes --bump here. It sizes the whole range
# and makes no lookup at all — a maintainer's statement needs no corroboration,
# and an outage must not be able to block the way out of an outage.
@test "--bump sizes the whole range and asks GitHub nothing" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): symbols'
  label_pr 118 release:major
  run bash -c "echo pkg | $SCRIPT --dry-run --bump=minor"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
  [[ "$output" == *"stated with --bump"* ]]
  [ "$(gh_calls)" -eq 0 ]
}

@test "--bump is the way past a refusal" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg $'feat(codec): symbols\n\nRelease-bump: minor'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  run bash -c "echo pkg | $SCRIPT --dry-run --bump=patch"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
}

@test "a --bump that is not a size is refused before anything runs" {
  run bash -c "echo pkg | $SCRIPT --dry-run --bump=huge"
  [ "$status" -eq 64 ]
  [[ "$output" == *"is not a size"* ]]
}

@test "--bump=major from a v1 base is still refused" {
  need_toolchain
  tag_release pkg/v1.2.3
  commit_pkg 'feat(codec): breaking'
  run bash -c "echo pkg | $SCRIPT --dry-run --bump=major"
  [ "$status" -ne 0 ]
  [[ "$output" == *"needs a real"* ]]
}

# ── ADR 0089: one notion of "a file that counts", shared with compute-bumps ───
#
# These run together on purpose. Each one alone passes under a rule that is
# wrong in the other direction, so a suite carrying only one of them would
# certify the defect it does not test.

# commit_internal <message> — touches internal/ and NOTHING under pkg/. Before
# ADR 0089 a `^pkg/` scope skipped its size, so a behavioural change that ADR
# 0007 §2 row 3 REQUIRES to be a minor could only ship as a patch — while
# compute-bumps.sh cut the release for it anyway, through rdeps.
commit_internal() {
  echo "// $RANDOM" >>internal/service/svc.go
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

# commit_pkg_doc <message> — touches ONLY a CLAUDE.md under pkg/.
# compute-bumps.sh refuses to cut a release for that churn, so it must not SIZE
# one either: that was the cheapest way past the old pkg/ scope.
commit_pkg_doc() {
  mkdir -p pkg/v1/foo
  echo "# $RANDOM" >>pkg/v1/foo/CLAUDE.md
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

@test "a label on an internal/-only merge sizes the release (ADR 0089)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_internal 'feat(service): behaviour observable through pkg'
  label_pr 120 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

@test "a label on a pkg/ CLAUDE.md-only merge does NOT size the release (ADR 0089)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg_doc 'docs: one line under pkg/'
  label_pr 121 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" != *"pkg/v0.2.0"* ]]
}

# The control. A rule answering "no" to everything would pass the test above;
# this is what stops that.
@test "a label on pkg/ code still sizes the release (ADR 0089 control)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg 'feat(codec): public symbols'
  label_pr 122 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.2.0"* ]]
}

# The refusal inherits the same scope: an internal/-only merge CAN size a
# release, so a request on one that nobody decided is exactly the silent patch
# the refusal exists to stop.
@test "an unlabelled request on an internal/-only merge is refused (ADR 0089)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_internal $'feat(service): behaviour\n\nRelease-bump: minor\n\nProse after the request.'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"refusing to size this release"* ]]
}

# commit_pkg_bench <message> — touches ONLY a BENCH.md under pkg/. Maintainer-only
# by lib/release-scope.sh's rule, so it can neither cut a release (it cut
# pkg/v0.4.4 — #238) nor size one.
commit_pkg_bench() {
  mkdir -p pkg/v1/foo
  printf 'BenchmarkFoo-8  %s ns/op\n' "$RANDOM" >>pkg/v1/foo/BENCH.md
  g add -A
  g commit -q --no-verify -F - <<<"$1"
}

@test "a label on a pkg/ BENCH.md-only merge does NOT size the release (#238)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg_bench 'docs(bench): re-measure'
  label_pr 123 release:minor
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v0.1.1"* ]]
  [[ "$output" != *"pkg/v0.2.0"* ]]
}

# And the refusal follows the same scope: a request on a merge that cannot size
# a release is an alarm about nothing, so it is neither looked up nor refused.
@test "an unlabelled request on a pkg/ BENCH.md-only merge is not an alarm (#238)" {
  need_toolchain
  tag_release pkg/v0.1.0
  commit_pkg_bench $'docs(bench): re-measure\n\nRelease-bump: minor\n\nProse after the request.'
  run bash -c "echo pkg | $SCRIPT --dry-run"
  [ "$status" -eq 0 ]
  [[ "$output" != *"refusing"* ]]
  [ "$(gh_calls)" -eq 0 ]
}

@test "an unwalkable --range is refused rather than read as no size" {
  run bash -c "echo pkg | $SCRIPT --dry-run --range=nosuchrev..HEAD"
  [ "$status" -eq 64 ]
  [[ "$output" == *"cannot walk the release range"* ]]
}

# ── lib/release-size.sh, the decision on its own ──────────────────────────────

@test "lib: text_ask reads a request anywhere, ranks it, and ignores non-sizes" {
  source "$SIZE_LIB"
  run text_ask <<<$'subject\n\nRelease-bump: minor\n\n* folded\n\nprose'
  [ "$output" = "minor" ]
  run text_ask <<<$'subject\n\nRelease-bump: minor\nRelease-bump: major'
  [ "$output" = "major" ]
  run text_ask <<<$'subject\n\nthe `Release-bump:` line in prose'
  [ -z "$output" ]
  run text_ask <<<$'subject\n\nRelease-bump: patch'
  [ -z "$output" ]
  run text_ask <<<$'subject\n\nRelease-bump: minor '
  [ "$output" = "minor" ]
}

@test "lib: label_size takes one release label, refuses two and refuses a non-size" {
  source "$SIZE_LIB"
  run label_size <<<$'bug\nrelease:minor'
  [ "$status" -eq 0 ]
  [ "$output" = "minor" ]
  run label_size <<<$'Release:Major'
  [ "$output" = "major" ]
  run label_size <<<$'release:minor\nrelease:minor'
  [ "$status" -eq 0 ]
  [ "$output" = "minor" ]
  run label_size <<<$'release:minor\nrelease:patch'
  [ "$status" -ne 0 ]
  run label_size <<<$'release:soon'
  [ "$status" -ne 0 ]
  run label_size <<<$'bug\ndocs'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "lib: decide_size — a label decides both ways, text alone is refused" {
  source "$SIZE_LIB"
  run decide_size patch minor
  [ "$output" = "patch" ]
  run decide_size major ""
  [ "$output" = "major" ]
  run decide_size "" minor
  [ "$status" -ne 0 ]
  run decide_size "" ""
  [ "$output" = "patch" ]
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
