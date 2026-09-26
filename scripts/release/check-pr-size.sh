#!/usr/bin/env bash
# scripts/release/check-pr-size.sh — ask a pull request, BEFORE it merges, the
# question cut-tags.sh asks of its merge after: what size would it publish?
# (ADR 0135). Run by .github/workflows/release-size.yml on every push and every
# label change of a pull request.
#
# Why it exists. cut-tags.sh refuses a merge whose message asks for more than a
# patch with no `release:*` label to decide it — the only answer that neither
# loses a maintainer's request (#217) nor lets contributor text size a release
# (#224). A refusal after the merge still stops the automatic release of
# everything behind it until a maintainer acts. This moves the same verdict to
# where acting costs one click: a label on an open pull request.
#
# The rule is lib/release-size.sh's, not a copy, over the same inputs the
# squash will produce: every commit of the branch, because
# `squash_merge_commit_message: COMMIT_MESSAGES` folds them all into the
# message cut-tags.sh reads. What it cannot see is an edit made to that message
# in the merge dialog; cut-tags.sh still refuses that one after the fact.
#
# Usage: check-pr-size.sh --pr=<number>
# Exit: 0 the size is decided, 1 it is not (the reason and the fix on stdout),
#       64 bad usage, 2 GitHub could not be read.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/release-scope.sh
. "$here/lib/release-scope.sh"
# shellcheck source=lib/release-size.sh
. "$here/lib/release-size.sh"

PR=""
for arg in "$@"; do
  case "$arg" in
    --pr=*) PR="${arg#--pr=}" ;;
    --help | -h)
      sed -n '2,24p' "$0"
      exit 0
      ;;
    *)
      echo "check-pr-size: unknown argument: $arg" >&2
      exit 64
      ;;
  esac
done

case "$PR" in
  "" | *[!0-9]*)
    echo "check-pr-size: --pr=<number> is required" >&2
    exit 64
    ;;
esac

if ! command -v "${RELEASE_GH:-gh}" >/dev/null 2>&1; then
  echo "check-pr-size: '${RELEASE_GH:-gh}' is not on PATH, so #$PR cannot be read" >&2
  exit 2
fi

repo="$(release_repo)"

# read_api <what> <path> <jq> — one paginated read; a failure is exit 2 and
# never an empty answer. An empty file list would read as "touches nothing that
# can cut a release" and pass the check: an absence of measurement standing in
# for a result, which is the class of defect this whole path is built against.
read_api() {
  local what="$1" path="$2" filter="$3" out="" rc=0
  out="$(release_gh api --paginate "$path" --jq "$filter")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "check-pr-size: could not read the $what of #$PR (gh exit $rc)" >&2
    exit 2
  fi
  printf '%s\n' "$out"
}

files="$(read_api files "repos/$repo/pulls/$PR/files" '.[].filename')"
if ! release_scope_any <<<"$files"; then
  echo "check-pr-size: #$PR touches nothing that can cut a release, so it sizes none (ADR 0089)."
  exit 0
fi

labels="$(read_api labels "repos/$repo/issues/$PR/labels" '.[].name')"
if ! lsize="$(label_size <<<"$labels" 2>&1)"; then
  echo "check-pr-size: #$PR: $lsize."
  exit 1
fi

messages="$(read_api commits "repos/$repo/pulls/$PR/commits" '.[].commit.message')"
ask="$(text_ask <<<"$messages")"

if size="$(decide_size "$lsize" "$ask")"; then
  if [ -n "$lsize" ] && [ -n "$ask" ] && [ "$ask" != "$lsize" ]; then
    echo "check-pr-size: #$PR: a commit asks for '$ask', the label says '$lsize' — the label decides."
  fi
  if [ -n "$lsize" ]; then
    echo "check-pr-size: #$PR would be released as a $size (label release:$lsize)."
  else
    echo "check-pr-size: #$PR would be released as a patch (no release label, and no commit asks for more)."
  fi
  exit 0
fi

cat <<EOF
check-pr-size: #$PR asks for 'Release-bump: $ask' in a commit message and carries no release label.

Merged like this, it would stop the automatic release (ADR 0135): a release is
sized by a maintainer's label on the pull request, never by message text,
because the squash message is composed from the branch commits and nothing in
it says who wrote a line. A maintainer settles it with ONE label:

  release:$ask    to grant the request
  release:patch    to decline it

A contributor who thinks a minor is due says so in the description; the size is
the maintainer's call.
EOF
exit 1
