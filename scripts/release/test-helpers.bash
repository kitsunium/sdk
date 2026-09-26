# scripts/release/test-helpers.bash — shared fixtures for the release suites,
# loaded with `load test-helpers`. Not a suite: release-scripts-test.sh globs
# `*.bats`, and this file is `.bash` so it is never run on its own.
#
# The release size is read from GitHub (ADR 0135), so the suites need a GitHub
# they control. install_gh_stub puts one in place of the real CLI: it answers
# `gh api <path> --jq <filter>` from JSON fixtures and runs the CALLER's filter
# over them with jq — so the jq expression the scripts ship is the one under
# test, not a paraphrase of it. Every call is appended to a log, which is how a
# suite proves a lookup did NOT happen: an absence the stub could not tell apart
# from a lookup that returned nothing.

# install_gh_stub — create the stub and point RELEASE_GH at it. RELEASE_GH and
# not PATH: the hosted Ubuntu runners ship gh in /usr/bin, so a PATH edit cannot
# guarantee which one runs.
install_gh_stub() {
  GH_FIXTURES="$BATS_TEST_TMPDIR/gh-fixtures"
  mkdir -p "$GH_FIXTURES"
  : >"$GH_FIXTURES/calls.log"
  cat >"$BATS_TEST_TMPDIR/gh" <<'STUB'
#!/usr/bin/env bash
# gh stub for the release suites. A fixture is named after the API path below
# repos/<owner>/<repo>/, with `/` as `_`: commits/<sha>/pulls is answered from
# commits_<sha>_pulls.json. <name>.fail makes the call fail like an HTTP error.
# A missing fixture is an empty list — a commit no pull request introduced.
set -euo pipefail
printf '%s\n' "$*" >>"$GH_FIXTURES/calls.log"
if [ "${1:-}" != api ]; then
  echo "gh stub: only 'gh api' is stubbed, got: $*" >&2
  exit 2
fi
shift
path="" filter="."
while [ "$#" -gt 0 ]; do
  case "$1" in
    --jq) filter="$2"; shift 2 ;;
    -H | --header | -X | --method) shift 2 ;;
    -*) shift ;;
    *) path="$1"; shift ;;
  esac
done
rest="${path#repos/}"
rest="${rest#*/}"
rest="${rest#*/}"
name="$(printf '%s' "$rest" | tr '/' '_')"
if [ -e "$GH_FIXTURES/$name.fail" ]; then
  echo "gh: Server Error (HTTP 502)" >&2
  exit 1
fi
if [ -f "$GH_FIXTURES/$name.json" ]; then
  jq -r "$filter" "$GH_FIXTURES/$name.json"
else
  printf '[]\n' | jq -r "$filter"
fi
STUB
  chmod +x "$BATS_TEST_TMPDIR/gh"
  export GH_FIXTURES
  export RELEASE_GH="$BATS_TEST_TMPDIR/gh"
  export RELEASE_REPO="kitsunium/sdk"
}

# label_pr <number> [label…] — record that the merged pull request <number>
# introduced HEAD and carries the given labels. No label is a pull request with
# none, which is a different fact from a commit no pull request introduced.
label_pr() {
  local number="$1" sha
  shift
  sha="$(git rev-parse HEAD)"
  jq -n --argjson n "$number" --arg sha "$sha" \
    '[{number: $n, merged_at: "2026-09-26T00:00:00Z", merge_commit_sha: $sha,
       labels: ($ARGS.positional | map({name: .}))}]' \
    --args "$@" >"$GH_FIXTURES/commits_${sha}_pulls.json"
}

# gh_calls — how many times the stub was asked anything.
gh_calls() {
  local n=0 _line
  while IFS= read -r _line; do
    n=$((n + 1))
  done <"$GH_FIXTURES/calls.log"
  echo "$n"
}
