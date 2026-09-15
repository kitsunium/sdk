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
  g config user.name  "ci"
  mkdir -p pkg/v1 internal/kernel/errs
  cat >pkg/go.mod <<'EOF'
module github.com/kitsunium/sdk/pkg

go 1.26
EOF
  : > pkg/v1/codec.go
  : > internal/kernel/errs/errs.go
  # A real module directory carries Bazel targets, and compute-bumps now uses
  # their PRESENCE to tell "this path has nothing to ask Bazel about" apart from
  # "the query failed". A fixture without one is not a module, it is the
  # CLAUDE.md case — which has its own test below.
  : > internal/kernel/errs/BUILD.bazel
  g add -A
  g commit -q --no-verify -m "init"
  g tag pkg/v0.1.0

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
  g commit -aq --no-verify -m "feat(v1): tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

@test "change to pkg/go.mod emits pkg" {
  printf '\n// bump\n' >> pkg/go.mod
  g commit -aq --no-verify -m "chore: module tweak"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

@test "bootstrap repo with no tags uses root commit as fallback" {
  cd "$(mktemp -d)"
  g init -q -b main
  g config user.email "ci@example.invalid"
  g config user.name  "ci"
  mkdir -p pkg/v1
  : > pkg/v1/x.go
  g add -A
  g commit -q --no-verify -m "first"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

@test "empty repo with no pkg dirs emits nothing" {
  cd "$(mktemp -d)"
  g init -q -b main
  g config user.email "ci@example.invalid"
  g config user.name  "ci"
  : > README.md
  g add -A
  g commit -q --no-verify -m "first"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "internal-only change without bazel emits nothing (graceful)" {
  if command -v bazel >/dev/null 2>&1; then skip "bazel present, rdeps path active"; fi
  echo "// tweak" >> internal/kernel/errs/errs.go
  g commit -aq --no-verify -m "fix(errs): wording"
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
  g commit -aq --no-verify -m "fix(errs): wording"
  PATH="$stub:$PATH" run "$SCRIPT"
  rm -rf "$stub"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

# --- #227: a failed query and an unreachable target used to be one silence ---
#
# The rdeps step read `grep -q . < <(bazel query … 2>/dev/null)`. stderr was
# discarded and the query's EXIT CODE was never consulted, so a query that
# FAILED and a query that did not reach //pkg/... both produced "no release".
# That is how c707de5 (#214) reached main with SDK Release green and no tag cut:
# step 6 ran bazel for 31s and emitted nothing, step 7 was skipped on
# `majors != ''`, and the workflow reported success.
#
# The three cases below are the three outcomes that must now be distinguishable.

# Case 1 of 3 — THE NEW DISTINCTION. A query that fails must fail the script,
# not quietly answer "no release". Without it this test is green against the old
# code for the wrong reason: the old code also emitted nothing here.
@test "a FAILED bazel query is refused loudly, not read as 'no release'" {
  stub="$(mktemp -d)"
  cat >"$stub/bazel" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  query)
    echo "ERROR: Skipping '//internal/kernel/...': error loading package" >&2
    exit 7
    ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$stub/bazel"
  echo "// tweak" >> internal/kernel/errs/errs.go
  g commit -aq --no-verify -m "fix(errs): wording"
  PATH="$stub:$PATH" run "$SCRIPT"
  rm -rf "$stub"
  # Loud: non-zero status, nothing on stdout that a caller could read as a
  # verdict, and the reason plus bazel's own stderr surfaced.
  [ "$status" -ne 0 ]
  [[ "$output" == *"bazel query failed (exit 7)"* ]]
  [[ "$output" == *"refusing to report 'no release'"* ]]
  [[ "$output" == *"error loading package"* ]]
}

# Case 2 of 3 — the over-correction guard. `awk -F/ '{print $1"/"$2}'` reduces a
# changed file to its module dir, which is right for internal/<mod>/go.mod but
# leaves a file ALREADY at depth two intact: internal/CLAUDE.md survives as the
# bogus label //internal/CLAUDE.md/... . Measured against the real repository
# that label is `exit 7, no targets found beneath` — the same exit code as case 1
# and the opposite meaning. Turning every non-zero exit red would fail a release
# on any commit that edits internal/CLAUDE.md, and that file is in the file list
# of ordinary commits on main.
#
# The stub fails on EVERY query, so this test passes only if the script never
# asks about a path with no Bazel targets beneath it.
@test "a path with no Bazel targets is skipped, never queried, never red" {
  stub="$(mktemp -d)"
  cat >"$stub/bazel" <<'STUB'
#!/usr/bin/env bash
echo "ERROR: no targets found beneath 'internal/CLAUDE.md'" >&2
exit 7
STUB
  chmod +x "$stub/bazel"
  printf '# module notes\n' > internal/CLAUDE.md
  g add -A
  g commit -q --no-verify -m "docs(internal): module notes"
  PATH="$stub:$PATH" run "$SCRIPT"
  rm -rf "$stub"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# Case 3 of 3 — a query that RAN and found nothing. Same stdout as case 1 and the
# same stdout as case 2, and it must keep meaning "no release". Without this row
# the fix could satisfy cases 1 and 2 by never emitting nothing at all.
@test "a query that succeeds and reaches nothing still means no release" {
  stub="$(mktemp -d)"
  cat >"$stub/bazel" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  query) exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$stub/bazel"
  echo "// tweak" >> internal/kernel/errs/errs.go
  g commit -aq --no-verify -m "fix(errs): wording"
  PATH="$stub:$PATH" run "$SCRIPT"
  rm -rf "$stub"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# Regression: the release tag lives on a DETACHED child of main (how cut-tags.sh
# publishes it), so `git describe` can't see it. compute-bumps must baseline on
# the tag's first parent, not fall back to root..HEAD and emit a spurious bump.
@test "detached release tag + no new main commits emits nothing (no spurious bump)" {
  cd "$(mktemp -d)"
  g init -q -b main
  g config user.email "ci@example.invalid"; g config user.name "ci"
  mkdir -p pkg/v1
  printf 'module github.com/kitsunium/sdk/pkg\n\ngo 1.26\n' > pkg/go.mod
  : > pkg/v1/codec.go
  g add -A; g commit -q --no-verify -m "init"
  base="$(g rev-parse HEAD)"
  # cut-tags-style: a detached commit whose parent is main HEAD, tagged, not on a branch.
  rel="$(g commit-tree "HEAD^{tree}" -p "$base" -m "release v0.1.0")"
  g tag pkg/v0.1.0 "$rel"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "detached release tag + a real pkg change after the base emits pkg" {
  cd "$(mktemp -d)"
  g init -q -b main
  g config user.email "ci@example.invalid"; g config user.name "ci"
  mkdir -p pkg/v1
  printf 'module github.com/kitsunium/sdk/pkg\n\ngo 1.26\n' > pkg/go.mod
  : > pkg/v1/codec.go
  g add -A; g commit -q --no-verify -m "init"
  base="$(g rev-parse HEAD)"
  rel="$(g commit-tree "HEAD^{tree}" -p "$base" -m "release v0.1.0")"
  g tag pkg/v0.1.0 "$rel"
  echo "// new" >> pkg/v1/codec.go
  g commit -aq --no-verify -m "feat(v1): real change after release"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

# --- #238: the exclusion was a two-name list under a comment describing a ------
# --- category, and it lived in two places that could disagree -----------------
#
# `pkg/v0.4.4` was cut from the merge of #237: ten files, every one a BENCH.md,
# zero lines of Go, an exported surface identical byte for byte to pkg/v0.4.3.
# A BENCH.md is maintainer-only metadata by the definition rule 1's own comment
# gives — it ships in the module zip and carries nothing a consumer can observe
# — and it simply was not among the two names the code listed, so
# `pkg/v1/errs/BENCH.md` fell through to `pkg/v*/*` and cut a release.
#
# These run together. The BENCH.md and USES.md rows fail against the pre-fix
# script; the README.md and .go rows pass against BOTH, and they are what stops
# a rule that answers "no" to everything from passing this section.

@test "a BENCH.md under pkg/ cuts no release — pkg/v0.4.4's witness (#238)" {
  mkdir -p pkg/v1/errs
  printf 'BenchmarkParse-8  12 ns/op\n' > pkg/v1/errs/BENCH.md
  g add -A
  g commit -q --no-verify -m "docs(bench): re-measure"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "a USES.md under pkg/ cuts no release — same category, another name" {
  mkdir -p pkg/v1/errs
  printf '# who uses this\n' > pkg/v1/errs/USES.md
  g add -A
  g commit -q --no-verify -m "docs(uses): note a consumer"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# The exception, and it is load-bearing rather than timid: pkg.go.dev renders a
# package's README and nothing else in the zip, and the docs portal copies
# pkg/<major>/**/README.md out of the RELEASE TAG (ADR 0007 §5). Excluding it
# would mean a README fix could never reach either surface until unrelated code
# cut a release.
@test "a README.md under pkg/ still cuts a release — the exception (control)" {
  mkdir -p pkg/v1/errs
  printf '# errs\n' > pkg/v1/errs/README.md
  g add -A
  g commit -q --no-verify -m "docs(errs): regenerate README"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

@test "a .go file under pkg/ still cuts a release (control)" {
  echo "// real change" >> pkg/v1/codec.go
  g commit -aq --no-verify -m "feat(v1): a public symbol"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}

# --- the half no list could have fixed: rule 2 -------------------------------
#
# Rule 1 had an exclusion and rule 2 had none, and rule 2 could not usefully
# have had one of its own: it reduces a changed path to `internal/<mod>` before
# asking anything, and after that reduction a BENCH.md and a .go file are the
# same module dir (#220 predicted this; it is measured here). So a CLAUDE.md
# under internal/ — a name rule 1 HAS excluded since before ADR 0089 — cut a
# release anyway, through the other rule.
#
# The stub records every invocation, so "the query did not run" is a counted
# fact rather than an absence of red: a missing bazel produces the same silence.

# bazel_subtree_fixture — a fresh repo whose INIT tree already contains a
# BUILD.bazel under internal/kernel, so the structural "is this a Bazel subtree"
# skip cannot mask the result, and a detached release tag as in production.
bazel_subtree_fixture() {
  cd "$(mktemp -d)"
  g init -q -b main
  g config user.email "ci@example.invalid"; g config user.name "ci"
  mkdir -p pkg/v1 internal/kernel/errs
  printf 'module github.com/kitsunium/sdk/pkg\n\ngo 1.26\n' > pkg/go.mod
  : > pkg/v1/codec.go
  : > internal/kernel/errs/errs.go
  printf 'go_library(name = "errs")\n' > internal/kernel/errs/BUILD.bazel
  g add -A; g commit -q --no-verify -m "init"
  local base rel
  base="$(g rev-parse HEAD)"
  rel="$(g commit-tree "HEAD^{tree}" -p "$base" -m "release v0.1.0")"
  g tag pkg/v0.1.0 "$rel"
}

# counting_bazel_stub <dir> <callfile> — answers every query with one row, and
# appends a line per invocation to <callfile>.
counting_bazel_stub() {
  cat >"$1/bazel" <<STUB
#!/usr/bin/env bash
echo "\$*" >> "$2"
case "\$1" in
  query) echo "//internal/kernel/errs:errs" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$1/bazel"
}

@test "a maintainer .md under internal/ is skipped before the query (#238/#220)" {
  bazel_subtree_fixture
  stub="$(mktemp -d)"; calls="$(mktemp)"
  counting_bazel_stub "$stub" "$calls"
  printf 'BenchmarkX-8  1 ns/op\n' > internal/kernel/errs/BENCH.md
  g add -A
  g commit -q --no-verify -m "docs(bench): re-measure internal"
  PATH="$stub:$PATH" run "$SCRIPT"
  ncalls="$(awk 'END { print NR + 0 }' "$calls")"
  rm -rf "$stub" "$calls"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  # The positive witness. Before, this was 1 call and the token "pkg".
  [ "$ncalls" -eq 0 ]
}

@test "an internal/ .go change is still queried and still cuts (rule 2 control)" {
  bazel_subtree_fixture
  stub="$(mktemp -d)"; calls="$(mktemp)"
  counting_bazel_stub "$stub" "$calls"
  echo "// real change" >> internal/kernel/errs/errs.go
  g commit -aq --no-verify -m "fix(errs): behaviour"
  PATH="$stub:$PATH" run "$SCRIPT"
  ncalls="$(awk 'END { print NR + 0 }' "$calls")"
  rm -rf "$stub" "$calls"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
  [ "$ncalls" -eq 1 ]
}

# --- #226: the log could not say why ------------------------------------------
#
# An internal/-only change published nothing and the reason was not recoverable
# from the run. stdout stays the contract ("pkg" or nothing) and the reason goes
# to stderr, so these tests read the two streams apart — `2>&1 >/dev/null`
# keeps stderr and discards stdout.

@test "--explain names the reason when nothing is published (#226)" {
  mkdir -p pkg/v1/errs
  printf 'BenchmarkParse-8  12 ns/op\n' > pkg/v1/errs/BENCH.md
  g add -A
  g commit -q --no-verify -m "docs(bench): re-measure"
  run bash -c "'$SCRIPT' --explain 2>&1 >/dev/null"
  [ "$status" -eq 0 ]
  [[ "$output" == *"changed paths: 1 (0 could carry a consumer-visible change)"* ]]
  [[ "$output" == *"every changed path is maintainer-only metadata"* ]]
  [[ "$output" == *"verdict: NO RELEASE"* ]]
}

@test "--explain names the rule that fired when something is published" {
  echo "// real change" >> pkg/v1/codec.go
  g commit -aq --no-verify -m "feat(v1): a public symbol"
  run bash -c "'$SCRIPT' --explain 2>&1 >/dev/null"
  [ "$status" -eq 0 ]
  [[ "$output" == *"rule 1: pkg/v1/codec.go is a public-module change -> bump"* ]]
  [[ "$output" == *"verdict: RELEASE"* ]]
}

# Rule 1 read its paths from `git diff --name-only … || true`, so a git that
# FAILED produced an empty path list and the verdict "no release" — the same
# shape #227 was opened for, one rule above it. cut-tags.sh already refuses an
# unwalkable range; this is the symmetric half.
@test "a range git cannot walk is refused, not read as 'no release'" {
  run "$SCRIPT" --range="nosuchrev..HEAD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"failed"* ]]
  [[ "$output" == *"refusing to report 'no release' for a range that could not be read"* ]]
}

# The `command -v bazel` guard is #227 defect 1 in different clothes: a missing
# bazel and an rdeps set reaching nothing produce the same empty stdout and the
# same verdict. A developer without bazel should still get rule 1, so the
# default stays a skip; the release lane, for which rule 2 is load-bearing,
# passes --require-bazel. PATH is narrowed to the system directories, which is
# where every tool the script needs lives and where bazel does not.
@test "--require-bazel refuses when bazel is absent and internal/ changed" {
  echo "// tweak" >> internal/kernel/errs/errs.go
  g commit -aq --no-verify -m "fix(errs): wording"
  PATH="/usr/bin:/bin" run "$SCRIPT" --require-bazel
  [ "$status" -ne 0 ]
  [[ "$output" == *"--require-bazel"* ]]
  [[ "$output" == *"would go unmeasured"* ]]
}

# The negative witness for the row above: --require-bazel must not fail a run
# that had no internal/ question to ask, or every docs merge would go red.
@test "--require-bazel is silent when no internal/ path changed" {
  echo "// real change" >> pkg/v1/codec.go
  g commit -aq --no-verify -m "feat(v1): a public symbol"
  PATH="/usr/bin:/bin" run "$SCRIPT" --require-bazel
  [ "$status" -eq 0 ]
  [ "$output" = "pkg" ]
}
