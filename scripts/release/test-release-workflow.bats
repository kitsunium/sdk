#!/usr/bin/env bats
# BATS tests for what sdk-release.yml decides around the scripts: the lock that
# keeps it from cutting anything until the repository variable RELEASE_ENABLED
# is `true`, the dry run a dispatch can ask for, and the notes of the GitHub
# release (release-notes.sh), whose tag classification goes through the
# anchored shapes of lib/tag-format.sh.
#
# The workflow is YAML that no runner here executes, so its cases read the
# file: the job's `if`, and the `run:` bodies of named steps. Each was seen red
# against the workflow as it was.

WF=""

setup() {
  WF="$BATS_TEST_DIRNAME/../../.github/workflows/sdk-release.yml"
  NOTES="$BATS_TEST_DIRNAME/release-notes.sh"
  LIST="$BATS_TEST_TMPDIR/tags.txt"
}

# job_if — the job-level `if:` of the release job, folded onto one line.
job_if() {
  awk '
    /^  release:/                 { injob = 1; next }
    injob && /^    if:/           { grab = 1; sub(/^    if: *>?-?/, ""); printf "%s ", $0; next }
    grab && /^    [A-Za-z_-]+:/   { exit }
    grab                          { printf "%s ", $0 }
  ' "$WF"
}

# step <name> — the lines of the step whose `- name:` is <name>, up to the next.
step() {
  awk -v want="      - name: $1" '
    $0 == want                    { grab = 1; print; next }
    grab && /^      - name:/      { exit }
    grab                          { print }
  ' "$WF"
}

# ── the lock ────────────────────────────────────────────────────────────────

# v0.19.6 and v0.19.7 were cut automatically on the day the module split was
# decided: an automatic run must cut nothing until a maintainer sets the
# repository variable, and the job's own `if` is where nothing at all runs.
@test "an automatic run needs RELEASE_ENABLED == 'true'" {
  cond="$(job_if)"
  [ -n "$cond" ]
  [[ "$cond" == *"vars.RELEASE_ENABLED == 'true'"* ]]
  # The variable guards the workflow_run branch: the push conclusion alone no
  # longer reaches the steps.
  [[ "$cond" == *"workflow_run.conclusion == 'success'"*"vars.RELEASE_ENABLED == 'true'"* ]] ||
    [[ "$cond" == *"vars.RELEASE_ENABLED == 'true'"*"workflow_run.conclusion == 'success'"* ]]
}

@test "a dispatch can ask for a dry run, and asks for one by default" {
  awk '/^  workflow_dispatch:/ { g = 1 } g' "$WF" >"$BATS_TEST_TMPDIR/dispatch"
  run awk '/^      dry_run:/ { g = 1; next } g && /^      [a-z_]+:/ { exit } g' "$BATS_TEST_TMPDIR/dispatch"
  [ "$status" -eq 0 ]
  [[ "$output" == *"type: boolean"* ]]
  [[ "$output" == *"default: true"* ]]
}

@test "a real cut by dispatch is refused while RELEASE_ENABLED is not true" {
  body="$(step "Refuse a cut while releases are locked")"
  [ -n "$body" ]
  [[ "$body" == *'RELEASE_ENABLED'* ]]
  [[ "$body" == *'exit 1'* ]]
}

@test "the dry run passes --dry-run to cut-tags.sh and publishes nothing" {
  body="$(step "Cut and push tags")"
  [[ "$body" == *'--dry-run'* ]]
  for s in "Verify the computed release was actually published" "Create the GitHub release"; do
    body="$(step "$s")"
    [ -n "$body" ]
    [[ "$body" == *"DRY_RUN"*"!= 'true'"* ]] || [[ "$body" == *"dry_run"* ]]
  done
}

# ── the notes ───────────────────────────────────────────────────────────────

# `/^(pkg|framework)\/v/` read pkg/v1/data/v0.1.0 as a tombstone: the classes
# are lib/tag-format.sh's anchored regexes, through release-notes.sh.
@test "the workflow classifies tags through release-notes.sh, never an unanchored regex" {
  run grep -n '(pkg|framework)\\/v/' "$WF"
  [ "$status" -ne 0 ]
  run grep -c 'scripts/release/release-notes.sh' "$WF"
  [ "$output" -ge 2 ]
}

@test "release-notes: a module tag under pkg/ is never a tombstone" {
  printf 'v0.20.0\npkg/v1/data/v0.1.0\ninternal/v0.1.0\n' >"$LIST"
  run bash "$NOTES" classify "$LIST"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'sdk v0.20.0\nmodule pkg/v1/data/v0.1.0\nmodule internal/v0.1.0')" ]
  run bash "$NOTES" notes "$LIST"
  [[ "$output" == *'- `pkg/v1/data/v0.1.0` — `github.com/kitsunium/sdk/pkg/v1/data`'* ]]
  [[ "$output" != *"holds no package"* ]]
}

@test "release-notes: the tombstones and the vendor tags are told apart" {
  printf 'v0.18.0\nthird-party/aws/v0.18.0\npkg/v0.18.0\nframework/v0.18.0\n' >"$LIST"
  run bash "$NOTES" classify "$LIST"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'sdk v0.18.0\nvendor third-party/aws/v0.18.0\ntombstone pkg/v0.18.0\ntombstone framework/v0.18.0')" ]
  run bash "$NOTES" count "$LIST"
  [ "$output" = "sdk=1 module=0 vendor=1 tombstone=2" ]
  run bash "$NOTES" notes "$LIST"
  [ "$status" -eq 0 ]
  [[ "$output" == *'- `third-party/aws/v0.18.0` — `github.com/kitsunium/sdk/third-party/aws`'* ]]
  [[ "$output" == *'- `pkg/v0.18.0` — `github.com/kitsunium/sdk/pkg`'* ]]
}

@test "release-notes: a tag no family accepts is refused, as is an empty list" {
  printf 'v0.20.0\nnot-a-tag\n' >"$LIST"
  run bash "$NOTES" classify "$LIST"
  [ "$status" -eq 1 ]
  [[ "$output" == *"'not-a-tag' is no tag a release cuts"* ]]
  : >"$LIST"
  run bash "$NOTES" notes "$LIST"
  [ "$status" -eq 1 ]
}

# ADR 0171: a release need not carry the SDK module.
@test "release-notes: a release of one family alone says the SDK module keeps its tag" {
  printf 'pkg/v1/data/v0.2.0\n' >"$LIST"
  run bash "$NOTES" notes "$LIST"
  [ "$status" -eq 0 ]
  [[ "$output" == *"did not change: it keeps its last tag"* ]]
  [[ "$output" == *'- `pkg/v1/data/v0.2.0` — `github.com/kitsunium/sdk/pkg/v1/data`'* ]]
  [[ "$output" != *'at ``'* ]]
}

# One GitHub release per run, on the manifest's release, the manifest attached
# and the published tags held to it.
@test "the workflow cuts with --manifest, checks it, and attaches it to the one release" {
  body="$(step "Cut and push tags")"
  [[ "$body" == *'--manifest=/tmp/release-manifest.json'* ]]
  body="$(step "Verify the computed release was actually published")"
  [[ "$body" == *'release-manifest.json'* ]]
  [[ "$body" == *'the manifest does not name the tags pushed'* ]]
  body="$(step "Create the GitHub release")"
  [[ "$body" == *'primary="$(jq -r '"'"'.release'"'"' /tmp/release-manifest.json)"'* ]]
  [[ "$body" == *'gh release create "$primary" /tmp/release-manifest.json'* ]]
  run grep -c 'gh release create' "$WF"
  [ "$output" = "1" ]
}

# ADR 0171 §2: the floor is asked of the proxy before anything is cut, in a dry
# run too.
@test "the workflow checks the version floor before the cut" {
  body="$(step "Check the version floor")"
  [[ "$body" == *'bash scripts/release/check-version-floor.sh'* ]]
  [[ "$body" != *'DRY_RUN'* ]]
  floor_line="$(grep -n 'name: Check the version floor' "$WF" | cut -d: -f1)"
  cut_line="$(grep -n 'name: Cut and push tags' "$WF" | cut -d: -f1)"
  [ "$floor_line" -lt "$cut_line" ]
}

# v0.18.0 to v0.28.0: every release listed each pull request since #130, with a
# "Full Changelog" to commits/<tag>. A tag names a release commit made off main,
# so the last release's tag is never its ancestor and GitHub, left to choose,
# chose no start. The start is the manifest's `previous` of the primary module.
@test "the workflow starts the generated notes at the primary module's previous tag" {
  body="$(step "Create the GitHub release")"
  [[ "$body" == *'start="$(bash scripts/release/release-notes.sh start-tag /tmp/release-manifest.json)"'* ]]
  [[ "$body" == *'from=(--notes-start-tag "$start")'* ]]
  [[ "$body" == *'--generate-notes ${from[@]+"${from[@]}"}'* ]]
}

@test "release-notes start-tag: the previous tag of the module the release is" {
  M="$BATS_TEST_TMPDIR/manifest.json"
  cat >"$M" <<'JSON'
{"release":"v0.28.0","modules":[
 {"dir":".","tag":"v0.28.0","previous":"v0.27.0"},
 {"dir":"pkg/v1/data","tag":"pkg/v1/data/v0.4.0","previous":"pkg/v1/data/v0.3.1"}],"tombstones":[]}
JSON
  run bash "$NOTES" start-tag "$M"
  [ "$status" -eq 0 ]
  [ "$output" = "v0.27.0" ]
  # A release of one family alone starts at that family's previous tag.
  cat >"$M" <<'JSON'
{"release":"pkg/v1/data/v0.4.0","modules":[{"dir":"pkg/v1/data","tag":"pkg/v1/data/v0.4.0","previous":"pkg/v1/data/v0.3.1"}],"tombstones":[]}
JSON
  run bash "$NOTES" start-tag "$M"
  [ "$status" -eq 0 ]
  [ "$output" = "pkg/v1/data/v0.3.1" ]
}

@test "release-notes start-tag: nothing for a first tag, a refusal for a release no module is" {
  M="$BATS_TEST_TMPDIR/manifest.json"
  printf '{"release":"internal/v0.1.0","modules":[{"dir":"internal","tag":"internal/v0.1.0","previous":""}],"tombstones":[]}' >"$M"
  run bash "$NOTES" start-tag "$M"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  printf '{"release":"v0.28.0","modules":[{"dir":"internal","tag":"internal/v0.2.0","previous":"internal/v0.1.0"}],"tombstones":[]}' >"$M"
  run bash "$NOTES" start-tag "$M"
  [ "$status" -eq 1 ]
  printf '{"release":"v0.28.0","modules":[{"dir":".","tag":"v0.28.0","previous":"--draft"}],"tombstones":[]}' >"$M"
  run bash "$NOTES" start-tag "$M"
  [ "$status" -eq 1 ]
  run bash "$NOTES" start-tag "$BATS_TEST_TMPDIR/absent.json"
  [ "$status" -eq 1 ]
}
