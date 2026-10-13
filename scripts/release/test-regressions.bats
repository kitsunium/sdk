#!/usr/bin/env bats
# Regression cases for two release issues that shipped a smaller version than
# a maintainer had asked for, each reproduced in the exact shape it shipped in
# and run through the whole pipeline an automatic run uses — compute-bumps.sh
# piped into cut-tags.sh, no --bump, the range from each module's own tag, as
# a workflow_run trigger runs it.
#
#   #271  PR #268, labelled release:minor, squash-merged as 96dfb083, cut as
#         v0.14.1. Its 196 files were all under framework/ but one docs file
#         and .ktn-linter.yaml; lib/release-scope.sh then counted pkg/ and
#         internal/ only (`return (p ~ /^pkg\//) || (p ~ /^internal\//)` at
#         1896ad23^), so range_size walked past the merge and never read its
#         label. Closed by #283 (1896ad23), which counts framework/ — NOT by
#         ADR 0135, which had already made the label the channel.
#   #217  3f681412 (pkg/v0.1.35) and e435af8c (pkg/v0.3.4): `Release-bump:
#         minor` buried mid-message by folded branch commits, read by a
#         parser of the LAST paragraph only, no label, shipped as a patch.
#         Closed by ADR 0135: the message is read anywhere, and an unlabelled
#         request above a patch is refused (exit 65) rather than dropped.
#
# Each case was seen red by reintroducing its defect into the current
# libraries (see the commit that added this file) and green against them.

load test-helpers

g() { git -c core.hooksPath="$NOHOOKS" "$@"; }

setup() {
  NOHOOKS="$BATS_TEST_TMPDIR/nohooks"
  mkdir -p "$NOHOOKS"
  REPO="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$REPO"
  cd "$REPO"
  g init -q -b main
  g config user.email "ci@example.invalid"
  g config user.name "ci"
  mkdir -p pkg/v1/resilience internal/service/resilience framework/model docs
  printf 'module github.com/kitsunium/sdk\n\ngo 1.27\n' >go.mod
  printf 'go 1.27\n\nuse .\n' >go.work
  echo "package resilience" >pkg/v1/resilience/doc.go
  echo "package resilience" >internal/service/resilience/retry.go
  printf 'go_library(name = "resilience")\n' >internal/service/resilience/BUILD.bazel
  echo "package model" >framework/model/model.go
  g add -A
  g commit -q --no-verify -m "init"
  # The release before the merge, on a detached child as cut-tags.sh cuts one.
  rel="$(g commit-tree "HEAD^{tree}" -p "$(g rev-parse HEAD)" -m "release v0.14.0")"
  g tag v0.14.0 "$rel"
  CB="$BATS_TEST_DIRNAME/compute-bumps.sh"
  CT="$BATS_TEST_DIRNAME/cut-tags.sh"
  install_gh_stub
}

need_toolchain() {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then skip "go/jq absent"; fi
}

would_tag() {
  awk -F'would tag: ' '/would tag: / { print $2 }' <<<"$output"
}

# #271 in its shape: a squash merge whose files are framework/ plus a docs
# file and a root config, labelled release:minor, cut by an automatic run.
@test "#271: a release:minor label on a framework/-only squash merge cuts a minor" {
  need_toolchain
  for f in model node_info privacy runtime table; do echo "// $f" >>"framework/model/$f.go"; done
  echo "notes" >docs/notes.md
  echo "linters: {}" >.ktn-linter.yaml
  g add -A
  g commit -q --no-verify -m "feat(framework): the model grows tables (#268)"
  label_pr 268 release:minor
  run bash -c "'$CB' --explain 2>/dev/null | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "v0.15.0" ]
  [[ "$output" == *"size minor for the SDK module — label release:minor on #268"* ]]
  [[ "$output" != *"no merge in the range is labelled above patch"* ]]
}

# The commit #217 lists first: a 126-line squash, the request at line 99, a
# folded branch commit's prose after it, the change under internal/ plus a
# CLAUDE.md under pkg/. No label: refused, never a silent patch.
msg_3f681412() {
  local i
  echo "feat(resilience): land the retry jitter ADR 0026 deferred (#185)"
  echo
  for i in $(seq 3 97); do echo "* fix(resilience): a folded branch commit, line $i"; done
  echo
  echo "Release-bump: minor"
  echo
  for i in $(seq 101 126); do echo "* docs(resilience): more prose after the request, line $i"; done
}

# bazel_reaches — a bazel on PATH whose rdeps query reaches a published
# package, as the real one does for internal/service/resilience: rule 2 is how
# an internal/-only change releases the SDK module, and the release lane runs
# it with --require-bazel.
bazel_reaches() {
  mkdir -p "$BATS_TEST_TMPDIR/bazel-bin"
  printf '#!/usr/bin/env bash\necho //pkg/v1/resilience:resilience\n' >"$BATS_TEST_TMPDIR/bazel-bin/bazel"
  chmod +x "$BATS_TEST_TMPDIR/bazel-bin/bazel"
  export PATH="$BATS_TEST_TMPDIR/bazel-bin:$PATH"
}

commit_3f681412() {
  bazel_reaches
  echo "// jitter" >>internal/service/resilience/retry.go
  echo "package resilience" >internal/service/resilience/retry_config.go
  echo "# retry" >>pkg/v1/resilience/CLAUDE.md
  g add -A
  g commit -q --no-verify -F <(msg_3f681412)
}

@test "#217: a request buried at line 99 of 126, unlabelled, is refused rather than shipped as a patch" {
  need_toolchain
  commit_3f681412
  [ "$(g log -1 --format=%B | awk '/^Release-bump:/ { print NR }')" = "99" ]
  # git's trailer parser reads the last paragraph only — the defect's root.
  [ -z "$(g log -1 --format='%(trailers:key=Release-bump,valueonly)')" ]
  run bash -c "'$CB' --require-bazel 2>/dev/null | '$CT' --dry-run"
  [ "$status" -eq 65 ]
  [[ "$output" == *"asks for 'Release-bump: minor' in its message"* ]]
  [[ "$output" != *"would tag"* ]]
}

@test "#217: the same merge labelled release:minor cuts the minor it asked for" {
  need_toolchain
  commit_3f681412
  label_pr 185 release:minor
  run bash -c "'$CB' --require-bazel 2>/dev/null | '$CT' --dry-run"
  [ "$status" -eq 0 ]
  [ "$(would_tag)" = "v0.15.0" ]
}
