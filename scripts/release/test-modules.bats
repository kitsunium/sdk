#!/usr/bin/env bats
# BATS tests for the release of several modules (ADR 0171): compute-bumps.sh
# naming each module that changed since its OWN last tag, cut-tags.sh sizing
# and tagging each at its OWN version — a label on one module's merge moves
# that module only —, the vendors tagged only when they changed (#295), the
# guards around the push (`git fetch --prune --prune-tags` first, EACH exact
# ref checked on origin before anything is tagged), the release-manifest.json
# that gives a release its identity, and the shell defects found on the way.
#
# The layout is the target's: the SDK module at the root, one shared
# `internal` module, two pkg families and one vendor module, each requiring
# what it imports through a local replace, as go.work's modules do.

load test-helpers

g() { git -c core.hooksPath="$NOHOOKS" "$@"; }

need_toolchain() {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then skip "go/jq absent"; fi
}

# gomod <dir> <require…> — a go.mod for the module in <dir>, requiring each
# module of this repository named (by directory, `.` the root) through a local
# replace, as the workspace does.
gomod() {
  local dir="$1" path rel dep dpath
  shift
  if [ "$dir" = "." ]; then path="github.com/kitsunium/sdk"; else path="github.com/kitsunium/sdk/$dir"; fi
  rel="$(sed -E 's#[^/]+#..#g' <<<"$dir")"
  {
    printf 'module %s\n\ngo 1.27\n' "$path"
    for dep in "$@"; do
      if [ "$dep" = "." ]; then dpath="github.com/kitsunium/sdk"; else dpath="github.com/kitsunium/sdk/$dep"; fi
      printf '\nrequire %s v0.0.0-00010101000000-000000000000\n\nreplace %s => %s/%s\n' "$dpath" "$dpath" "$rel" "$dep"
    done
  } >"$dir/go.mod"
}

setup() {
  NOHOOKS="$BATS_TEST_TMPDIR/nohooks"
  mkdir -p "$NOHOOKS"
  REPO="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$REPO"
  cd "$REPO"
  g init -q -b main
  # Every commit and every push spawns `git maintenance run --auto --detach`
  # (git 2.47 measured), which outlives the command and takes
  # objects/maintenance.lock: a teardown's `rm -rf` racing it fails with
  # "objects: Directory not empty". A disposable repository needs no upkeep.
  g config maintenance.auto false
  g config user.email "ci@example.invalid"
  g config user.name "ci"
  mkdir -p framework/kit internal/kernel/x pkg/v1/data pkg/v1/net third-party/aws
  printf 'module github.com/kitsunium/sdk\n\ngo 1.27\n' >go.mod
  gomod internal
  gomod pkg/v1/data internal
  gomod pkg/v1/net internal
  gomod third-party/aws .
  echo "package kit" >framework/kit/kit.go
  echo "package x" >internal/kernel/x/x.go
  echo "package data" >pkg/v1/data/data.go
  echo "package net" >pkg/v1/net/net.go
  echo "package aws" >third-party/aws/aws.go
  printf 'go 1.27\n\nuse (\n\t.\n\t./internal\n\t./pkg/v1/data\n\t./pkg/v1/net\n\t./third-party/aws\n)\n' >go.work
  g add -A
  g commit -q --no-verify -m "the layout of ADR 0171"
  CB="$BATS_TEST_DIRNAME/compute-bumps.sh"
  CT="$BATS_TEST_DIRNAME/cut-tags.sh"
  install_gh_stub
}

# released — one release commit, a detached child of HEAD, carrying the tags of
# a published history: the root at v0.19.7 and the vendor at v0.19.6 (the
# floors of ADR 0171), the split modules at v0.1.0; and origin, holding them.
released() {
  local rel
  rel="$(g commit-tree "HEAD^{tree}" -p "$(g rev-parse HEAD)" -m "release")"
  for t in v0.19.7 third-party/aws/v0.19.6 internal/v0.1.0 pkg/v1/data/v0.1.0 pkg/v1/net/v0.1.0; do
    g tag -a "$t" -m "$t" "$rel"
  done
  g init -q --bare "$BATS_TEST_TMPDIR/origin.git"
  g -C "$BATS_TEST_TMPDIR/origin.git" config maintenance.auto false
  g -C "$BATS_TEST_TMPDIR/origin.git" config receive.autogc false
  g remote add origin "$BATS_TEST_TMPDIR/origin.git"
  g push -q origin --tags
}

# change <file> <message> — one first-parent commit touching <file>.
change() {
  echo "// $RANDOM" >>"$1"
  g commit -aq --no-verify -m "$2"
}

would_tag() {
  awk -F'would tag: ' '/would tag: / { print $2 }' <<<"$output"
}

remote_tags() {
  g ls-remote --tags --refs origin | awk '{ sub(/^refs\/tags\//, "", $2); print $2 }' | LC_ALL=C sort | paste -sd' ' -
}

# ── which modules ───────────────────────────────────────────────────────────

@test "compute-bumps names the one module that changed, not the SDK module" {
  released
  change pkg/v1/data/data.go "fix(data): one"
  run "$CB"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg/v1/data" ]
}

@test "each module is measured from its own last tag" {
  released
  change pkg/v1/net/net.go "fix(net): one"
  rel="$(g commit-tree "HEAD^{tree}" -p "$(g rev-parse HEAD)" -m "release net")"
  g tag pkg/v1/net/v0.1.1 "$rel"
  change pkg/v1/data/data.go "fix(data): one"
  run "$CB"
  [ "$output" = "pkg/v1/data" ]
}

# Rule 1i (ADR 0171 §7): a family pins internal at the version its own release
# gave it, so a fix in internal reaches a consumer of the family only through a
# release of the family. A module whose packages import a package internal
# changed is released with it; one that does not keeps its tag.
@test "a module importing a package internal changed is released with it, and only it (rule 1i)" {
  need_toolchain
  printf 'package data\n\nimport _ "github.com/kitsunium/sdk/internal/kernel/x"\n' >pkg/v1/data/data.go
  for m in go.mod internal/go.mod pkg/v1/data/go.mod pkg/v1/net/go.mod third-party/aws/go.mod; do
    sed -i.bak 's/^go 1.27$/go 1.21/' "$m" && rm -f "$m.bak"
  done
  g commit -aq --no-verify -m "data imports internal/kernel/x"
  released
  change internal/kernel/x/x.go "fix(x): one"
  run bash -c "'$CB' 2>/dev/null"
  [ "$status" -eq 0 ]
  [ "$(paste -sd' ' - <<<"$output")" = "internal pkg/v1/data" ]
  run "$CB" --explain
  [[ "$output" == *"rule 1i: pkg/v1/data imports a package internal changed"* ]]
  run bash -c "echo sdk | '$CT' --dry-run --bump=patch"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "v0.19.8 internal/v0.1.1 pkg/v1/data/v0.1.1" ]
  block="$(awk '/--- pkg\/v1\/data\/go.mod ---/ { on = 1; next } /--- / { on = 0 } on' <<<"$output")"
  [[ "$block" == *"github.com/kitsunium/sdk/internal v0.1.1"* ]]
}

# ── how big, per module ─────────────────────────────────────────────────────

# The defect a module per family exists to end: one label moving everything.
@test "a label on one family's merge sizes that family, and only it" {
  need_toolchain
  released
  change pkg/v1/data/data.go "feat(data): new API"
  label_pr 301 release:minor
  change pkg/v1/net/net.go "fix(net): a fix"
  label_pr 302
  run bash -c "'$CB' | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "pkg/v1/data/v0.2.0 pkg/v1/net/v0.1.1" ]
  [[ "$output" == *"--- pkg/v1/data/go.mod ---"* ]]
  [[ "$output" == *"size minor for pkg/v1/data — label release:minor on #301"* ]]
  [[ "$output" == *"size patch for pkg/v1/net"* ]]
}

@test "a merge touching two families sizes both from one lookup" {
  need_toolchain
  released
  echo "// a" >>pkg/v1/data/data.go
  echo "// b" >>pkg/v1/net/net.go
  g commit -aq --no-verify -m "feat: both"
  label_pr 303 release:minor
  run bash -c "'$CB' | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "pkg/v1/data/v0.2.0 pkg/v1/net/v0.2.0" ]
  [ "$(gh_calls)" -eq 1 ]
}

# Each module continues its own numbering: the vendor at v0.19.6 is cut at
# v0.19.7 although the root's v0.19.7 exists — another module's tag.
@test "a module's version continues its own last tag" {
  need_toolchain
  released
  change third-party/aws/aws.go "fix(aws): one"
  run bash -c "'$CB' | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "third-party/aws/v0.19.7" ]
  # Its go.mod pins the SDK module at the SDK's own last tag.
  [[ "$output" == *"require github.com/kitsunium/sdk v0.19.7"* ]]
}

# A family that requires the internal module pins it at the version THIS
# release tags it at, or at its own last tag when it is not released.
@test "a family pins the internal module at its new version, or at its last tag" {
  need_toolchain
  released
  change pkg/v1/data/data.go "fix(data): one"
  run bash -c "'$CB' | '$CT' --dry-run"
  [[ "$output" == *"github.com/kitsunium/sdk/internal v0.1.0"* ]]
  change internal/kernel/x/x.go "fix(x): one"
  run bash -c "'$CB' | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "internal/v0.1.1 pkg/v1/data/v0.1.1" ]
  [[ "$output" == *"github.com/kitsunium/sdk/internal v0.1.1"* ]]
  [[ "$output" != *"=> ../"* ]]
}

# #295: v0.19.0 tagged every vendor module; under `sdk` (ADR 0162's release)
# one whose only change is a test keeps its tag.
@test "the sdk token tags a vendor module only when it changed (#295)" {
  need_toolchain
  released
  echo "package aws" >third-party/aws/aws_gen_test.go
  g add -A
  g commit -q --no-verify -m "test(aws): the generated pins"
  change pkg/v1/data/data.go "fix(data): one"
  run bash -c "echo sdk | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "v0.19.8 pkg/v1/data/v0.1.1" ]
  [[ "$output" == *"third-party/aws unchanged since third-party/aws/v0.19.6 — keeps it"* ]]
}

@test "a token naming no module of go.work is refused" {
  run bash -c "echo pkg/v1/nope | '$CT' --dry-run"
  [ "$status" -eq 1 ]
  [[ "$output" == *"unexpected bump token 'pkg/v1/nope'"* ]]
}

@test "release_modules accepts the layout's modules and refuses a retired one" {
  run bash -c ". '$BATS_TEST_DIRNAME/lib/tag-format.sh'; . '$BATS_TEST_DIRNAME/lib/release-modules.sh'; release_modules"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf '.\ninternal\npkg/v1/data\npkg/v1/net\nthird-party/aws')" ]
  printf 'go 1.27\n\nuse (\n\t.\n\t./internal/core\n)\n' >go.work
  run bash -c ". '$BATS_TEST_DIRNAME/lib/tag-format.sh'; . '$BATS_TEST_DIRNAME/lib/release-modules.sh'; release_modules"
  [ "$status" -eq 1 ]
  [[ "$output" == *"uses internal/core, which no release can tag"* ]]
}

# ── the push ────────────────────────────────────────────────────────────────

@test "a release pushes every module's tag in one atomic push, on one release commit" {
  need_toolchain
  released
  change pkg/v1/data/data.go "fix(data): one"
  change third-party/aws/aws.go "fix(aws): one"
  head="$(g rev-parse HEAD)"
  run bash -c "'$CB' | '$CT' 2>/dev/null"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'pkg/v1/data/v0.1.1\nthird-party/aws/v0.19.7')" ]
  [ "$(g rev-parse 'pkg/v1/data/v0.1.1^{commit}')" = "$(g rev-parse 'third-party/aws/v0.19.7^{commit}')" ]
  [ "$(g rev-parse 'pkg/v1/data/v0.1.1^{commit}^1')" = "$head" ]
  [[ "$(remote_tags)" == *"pkg/v1/data/v0.1.1"*"third-party/aws/v0.19.7"* ]]
}

# The tags this run reads are origin's: a tag origin does not hold is pruned
# before it can be read as a module's last release.
@test "the cut fetches with --prune-tags first, so a stray local tag is no base" {
  need_toolchain
  released
  rel="$(g commit-tree "HEAD^{tree}" -p "$(g rev-parse HEAD)" -m "stray")"
  g tag pkg/v1/data/v0.9.0 "$rel"
  change pkg/v1/data/data.go "fix(data): one"
  run bash -c "'$CB' | '$CT' 2>&1"
  [ "$status" -eq 0 ]
  [[ "$output" == *"pkg/v1/data/v0.1.0 -> pkg/v1/data/v0.1.1"* ]]
  [ -z "$(g tag -l pkg/v1/data/v0.9.0)" ]
}

# race_shim — a `git` in front of the real one that, on the first `ls-remote`
# the cut runs, tags pkg/v1/data/v0.1.1 on origin as another run would have,
# AFTER the fetch and BEFORE the push.
race_shim() {
  local real
  real="$(command -v git)"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cat >"$BATS_TEST_TMPDIR/bin/git" <<SHIM
#!/usr/bin/env bash
if [ "\$1" = ls-remote ] && [ ! -e "$BATS_TEST_TMPDIR/raced" ]; then
  : >"$BATS_TEST_TMPDIR/raced"
  "$real" --git-dir="$BATS_TEST_TMPDIR/origin.git" tag $1 "\$("$real" --git-dir="$BATS_TEST_TMPDIR/origin.git" rev-parse v0.19.7^{commit})"
fi
exec "$real" "\$@"
SHIM
  chmod +x "$BATS_TEST_TMPDIR/bin/git"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

@test "a tag of the release that origin acquired after the fetch aborts the push (exit 2)" {
  need_toolchain
  released
  change pkg/v1/data/data.go "fix(data): one"
  n_before="$(g ls-remote --tags --refs origin | awk 'END { print NR }')"
  race_shim pkg/v1/data/v0.1.1
  run bash -c "'$CB' | '$CT'"
  [ "$status" -eq 2 ]
  [[ "$output" == *"race detected — origin already holds pkg/v1/data/v0.1.1"* ]]
  [[ "$output" == *"nothing was published"* ]]
  # Origin holds the other run's tag and nothing of this one.
  [ "$(g ls-remote --tags --refs origin | awk 'END { print NR }')" -eq $((n_before + 1)) ]
  [ -z "$(g tag -l pkg/v1/data/v0.1.1)" ]
}

# ls-remote matches a pattern against a ref's TAIL: a tag whose name ends in
# the release's tag is no race. Origin holds pin/v0.19.8, which a pattern
# `v0.19.8` would answer.
@test "the race guard compares exact refs, not a tail" {
  need_toolchain
  released
  g --git-dir="$BATS_TEST_TMPDIR/origin.git" tag pin/v0.19.8 "$(g rev-parse 'v0.19.7^{commit}')"
  change framework/kit/kit.go "fix(kit): one"
  run bash -c "'$CB' | '$CT' 2>/dev/null"
  [ "$status" -eq 0 ]
  [ "$output" = "v0.19.8" ]
}

# ── the floor on the root (ADR 0171 §7) ─────────────────────────────────────

# monolith — the published history before the split: the root at v0.19.7 on a
# tree where internal/ and the families hold no go.mod (they were the root's
# packages), the vendor at v0.19.6; and origin, holding them. No module of the
# split has a tag yet.
monolith() {
  local tree rel idx="$BATS_TEST_TMPDIR/index"
  GIT_INDEX_FILE="$idx" g read-tree HEAD
  GIT_INDEX_FILE="$idx" g rm -q --cached internal/go.mod pkg/v1/data/go.mod pkg/v1/net/go.mod
  tree="$(GIT_INDEX_FILE="$idx" g write-tree)"
  rel="$(g commit-tree "$tree" -p "$(g rev-parse HEAD)" -m "release v0.19.7")"
  g tag -a v0.19.7 -m v0.19.7 "$rel"
  g tag -a third-party/aws/v0.19.6 -m third-party/aws/v0.19.6 "$rel"
  g init -q --bare "$BATS_TEST_TMPDIR/origin.git"
  g -C "$BATS_TEST_TMPDIR/origin.git" config maintenance.auto false
  g -C "$BATS_TEST_TMPDIR/origin.git" config receive.autogc false
  g remote add origin "$BATS_TEST_TMPDIR/origin.git"
  g push -q origin --tags
}

# The first split release: internal and each family require the root at the
# version the same release gives it, the first that holds neither pkg/ nor
# internal/ — a consumer pinned to v0.19.7 who adds a family is lifted to it.
@test "the first split release has internal and every family require the root it cuts" {
  need_toolchain
  monolith
  run bash -c "echo sdk | '$CT' --dry-run --bump=minor"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "v0.20.0 internal/v0.1.0 pkg/v1/data/v0.1.0 pkg/v1/net/v0.1.0 internal/kernel/v0.18.0 internal/core/v0.18.0 internal/service/v0.18.0" ]
  for m in internal pkg/v1/data pkg/v1/net; do
    block="$(awk -v m="--- $m/go.mod ---" '$0 ~ m { on = 1; next } /--- / { on = 0 } on' <<<"$output")"
    [[ "$block" == *"github.com/kitsunium/sdk v0.20.0"* ]]
  done
}

# A vendor module imports internal's packages, which a monolithic root holds
# too: it carries the floor as a family does.
@test "a vendor module released with the split requires the root it cuts" {
  need_toolchain
  gomod third-party/aws internal
  g commit -aq --no-verify -m "aws requires internal, as the split's vendors do"
  monolith
  change third-party/aws/aws.go "fix(aws): one"
  run bash -c "echo sdk | '$CT' --dry-run --bump=minor"
  [ "$status" -eq 0 ]
  [[ "$(would_tag)" == *"third-party/aws/v0.20.0"* ]]
  block="$(awk '/--- third-party\/aws\/go.mod ---/ { on = 1; next } /--- / { on = 0 } on' <<<"$output")"
  [[ "$block" == *"github.com/kitsunium/sdk v0.20.0"* ]]
}

# B1: after the split a `go get -u` from a monolithic root resolved an
# internal package to …/internal/kernel@v0.17.0, the longest path that still
# provided it. The split's release cuts a tombstone of each: a go.mod alone,
# requiring the internal module it tags, in the same atomic push.
@test "the split's release cuts the tombstones of the three internal modules, in its push" {
  need_toolchain
  monolith
  run bash -c "echo sdk | '$CT' --bump=minor --allow-bootstrap 2>&1"
  [ "$status" -eq 0 ]
  for m in kernel core service; do
    [[ "$(remote_tags)" == *"internal/$m/v0.18.0"* ]]
    [ "$(g ls-tree -r --name-only "internal/$m/v0.18.0" -- "internal/$m/")" = "internal/$m/go.mod" ]
    g show "internal/$m/v0.18.0:internal/$m/go.mod" >"$BATS_TEST_TMPDIR/t.mod"
    [ "$(go mod edit -json "$BATS_TEST_TMPDIR/t.mod" | jq -c '[.Module.Path, (.Require | map(.Path + "@" + .Version))]')" = \
      "[\"github.com/kitsunium/sdk/internal/$m\",[\"github.com/kitsunium/sdk/internal@v0.1.0\"]]" ]
    [[ "$(go mod edit -json "$BATS_TEST_TMPDIR/t.mod" | jq -r .Module.Deprecated)" == *"go get github.com/kitsunium/sdk/pkg/v1/<family>@latest for each family you import && go mod tidy"* ]]
    [ "$(g rev-parse "internal/$m/v0.18.0^{commit}^1")" = "$(g rev-parse 'v0.20.0^{commit}')" ]
    # and the internal module requires each tombstone back, so a build that
    # still lists the retired module is lifted to it (a cycle Go accepts).
    [[ "$(g show 'internal/v0.1.0:internal/go.mod')" == *"github.com/kitsunium/sdk/internal/$m v0.18.0"* ]]
  done
}

# I1: a consumer still on …/pkg v0.17.0 could not add a family — two modules
# provided its packages. Every family requires …/pkg at its tombstone, and
# every module that requires internal the split's tombstones, so one `go get`
# of the family lifts both off the old modules (measured on a file proxy).
@test "a family requires …/pkg at its tombstone and the split's tombstones once they exist" {
  need_toolchain
  released
  blob="$(printf 'module github.com/kitsunium/sdk/pkg\n' | g hash-object -w --stdin)"
  sub="$(printf '100644 blob %s\tgo.mod\n' "$blob" | g mktree)"
  root="$(printf '040000 tree %s\tpkg\n' "$sub" | g mktree)"
  g tag pkg/v0.18.0 "$(g commit-tree "$root" -m "pkg tombstone")"
  for m in kernel core service; do g tag "internal/$m/v0.18.0" "$(g commit-tree "$root" -m "$m tombstone")"; done
  change pkg/v1/data/data.go "fix(data): one"
  run bash -c "printf 'pkg/v1/data\n' | '$CT' --dry-run --bump=patch"
  [ "$status" -eq 0 ]
  block="$(awk '/--- pkg\/v1\/data\/go.mod ---/ { on = 1; next } /--- / { on = 0 } on' <<<"$output")"
  [[ "$block" == *"github.com/kitsunium/sdk/pkg v0.18.0"* ]]
  for m in kernel core service; do
    [[ "$block" == *"github.com/kitsunium/sdk/internal/$m v0.18.0"* ]]
  done
}

# I2: the split's release is a bootstrap — held without --allow-bootstrap
# (exit 3, nothing tagged), refused as a patch (exit 65), even in a dry run.
@test "the split's release is held without --allow-bootstrap and refused as a patch" {
  need_toolchain
  monolith
  run bash -c "echo sdk | '$CT' --dry-run --bump=patch"
  [ "$status" -eq 65 ]
  [[ "$output" == *"as the split's release"* ]]
  run bash -c "echo sdk | '$CT' --bump=minor"
  [ "$status" -eq 3 ]
  [[ "$output" == *"refusing to auto-cut the split's release"* ]]
  [ -z "$(g tag -l 'internal/*')" ]
  [[ "$(remote_tags)" != *"v0.20.0"* ]]
}

# A family cut before any root holds the split has no floor to require: it
# would publish a module a consumer on v0.19.7 cannot add. Refused, naming why.
@test "a family cut while no root holds the split is refused" {
  need_toolchain
  monolith
  run bash -c "printf 'internal\npkg/v1/data\n' | '$CT' --dry-run --bump=patch"
  [ "$status" -eq 1 ]
  [[ "$output" == *"needs the root's floor (ADR 0171 §7)"* ]]
  [[ "$output" != *"would tag"* ]]
}

# Once a root holds the split, the floor is that root for good — a later
# family release does not follow the root's newer versions.
@test "a later release keeps the floor at the first root that held the split" {
  need_toolchain
  released
  rel="$(g commit-tree "HEAD^{tree}" -p "$(g rev-parse HEAD)" -m "release v0.19.9")"
  g tag -a v0.19.9 -m v0.19.9 "$rel"
  change pkg/v1/data/data.go "fix(data): one"
  run bash -c "printf 'pkg/v1/data\n' | '$CT' --dry-run --bump=patch"
  [ "$status" -eq 0 ]
  [[ "$output" == *"github.com/kitsunium/sdk v0.19.7"* ]]
  [[ "$output" != *"github.com/kitsunium/sdk v0.19.9"* ]]
}

# --no-push is the rehearsal's cut: the release commit and every tag made, the
# exact-ref check run, the manifest written — and origin left as it was.
@test "--no-push tags the release commit here and pushes nothing" {
  need_toolchain
  released
  change pkg/v1/data/data.go "fix(data): one"
  before="$(remote_tags)"
  m="$BATS_TEST_TMPDIR/release-manifest.json"
  run bash -c "'$CB' | '$CT' --no-push --manifest='$m'"
  [ "$status" -eq 0 ]
  [[ "$output" == *"NO-PUSH: tagged pkg/v1/data/v0.1.1"* ]]
  [ "$(remote_tags)" = "$before" ]
  [ "$(g rev-parse 'pkg/v1/data/v0.1.1^{commit}^1')" = "$(g rev-parse HEAD)" ]
  [ "$(jq -r '.pushed' "$m")" = "false" ]
  [ "$(jq -r '.dry_run' "$m")" = "false" ]
  [ "$(jq -r '.root_floor' "$m")" = "v0.19.7" ]
  [ "$(jq -r '.release_commit' "$m")" = "$(g rev-parse 'pkg/v1/data/v0.1.1^{commit}')" ]
  g show 'pkg/v1/data/v0.1.1:pkg/v1/data/go.mod' >"$BATS_TEST_TMPDIR/data.mod"
  [[ "$(cat "$BATS_TEST_TMPDIR/data.mod")" == *"github.com/kitsunium/sdk v0.19.7"* ]]
  run bash -c "echo sdk | '$CT' --dry-run --no-push"
  [ "$status" -eq 64 ]
}

# ── the manifest ────────────────────────────────────────────────────────────

@test "--manifest writes the release's identity, in a dry run and for real" {
  need_toolchain
  released
  change pkg/v1/data/data.go "feat(data): one"
  label_pr 310 release:minor
  change third-party/aws/aws.go "fix(aws): one"
  m="$BATS_TEST_TMPDIR/release-manifest.json"
  run bash -c "'$CB' | '$CT' --dry-run --manifest='$m'"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.release' "$m")" = "pkg/v1/data/v0.2.0" ]
  [ "$(jq -r '.dry_run' "$m")" = "true" ]
  [ "$(jq -r '.release_commit' "$m")" = "" ]
  [ "$(jq -c '[.modules[] | [.dir, .module, .tag, .version, .previous, .size]]' "$m")" = \
    '[["pkg/v1/data","github.com/kitsunium/sdk/pkg/v1/data","pkg/v1/data/v0.2.0","v0.2.0","pkg/v1/data/v0.1.0","minor"],["third-party/aws","github.com/kitsunium/sdk/third-party/aws","third-party/aws/v0.19.7","v0.19.7","third-party/aws/v0.19.6","patch"]]' ]
  [ "$(jq -c '.tombstones' "$m")" = "[]" ]
  run bash -c "'$CB' | '$CT' --manifest='$m' 2>/dev/null"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.dry_run' "$m")" = "false" ]
  [ "$(jq -r '.commit' "$m")" = "$(g rev-parse HEAD)" ]
  [ "$(jq -r '.release_commit' "$m")" = "$(g rev-parse 'pkg/v1/data/v0.2.0^{commit}')" ]
}

# ── defects found on the way ────────────────────────────────────────────────

# cut-tags.sh:207 fed `git log` to the classifier through a process
# substitution, whose failure nothing saw: a commit whose paths git could not
# read counted as one that touched nothing, and its label was dropped. The
# shim fails the per-commit read (a diff against the first parent since the
# per-module scope).
@test "a commit whose paths git cannot read refuses the size, never reads as untouched" {
  need_toolchain
  released
  change pkg/v1/data/data.go "feat(data): one"
  label_pr 320 release:minor
  real="$(command -v git)"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cat >"$BATS_TEST_TMPDIR/bin/git" <<SHIM
#!/usr/bin/env bash
if [ "\$1" = diff ] && [ "\$2" = --name-only ]; then echo "fatal: bad object" >&2; exit 128; fi
exec "$real" "\$@"
SHIM
  chmod +x "$BATS_TEST_TMPDIR/bin/git"
  run bash -c "echo pkg/v1/data | PATH='$BATS_TEST_TMPDIR/bin:$PATH' '$CT' --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"could not be read — refusing to size this release"* ]]
  [[ "$output" != *"would tag"* ]]
}

# compute-bumps.sh iterated its path list through an unquoted array expansion
# under nullglob; the paths now go through `while read`. A path carrying a
# space or a glob character is one path, and counts.
@test "a path with a space or a glob character is one path that counts" {
  released
  printf 'package data\n' >'pkg/v1/data/a [b] *.go'
  g add -A
  g commit -q --no-verify -m "feat(data): an odd name"
  run "$CB" --explain
  [ "$status" -eq 0 ]
  [[ "$output" == *"module pkg/v1/data: changed paths: 1 (1 could carry"* ]]
  [[ "$output" == *$'\npkg/v1/data'* ]]
}

# tag-format.sh:351 cut the root commits with `| head -n1` under pipefail; a
# history with several roots is answered by its first one, and never fails.
@test "release_base answers a history with several root commits" {
  g checkout -q --orphan other
  g rm -rqf .
  echo x >other.txt
  g add other.txt
  g commit -q --no-verify -m "an unrelated root"
  g checkout -q main
  g merge -q --no-ff --no-edit --allow-unrelated-histories other >/dev/null
  run bash -c ". '$BATS_TEST_DIRNAME/lib/tag-format.sh'; release_base"
  [ "$status" -eq 0 ]
  [ -n "$output" ]
  [[ "$(g rev-list --max-parents=0 HEAD)" == *"$output"* ]]
}
