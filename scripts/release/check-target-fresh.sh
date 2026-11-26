#!/usr/bin/env bash
#
# check-target-fresh.sh — is the commit a release would tag still safe to tag?
#
#   check-target-fresh.sh <target-sha> [<main-ref>]     (main-ref: origin/main)
#
# GitHub judges a tag pushed by an App against the default branch: when the
# tagged commit's .github/workflows differs from main's, the push counts as an
# update to a workflow file and is refused without the `workflows` permission,
# which the release App does not have and is not given (#1497, the owner's
# decision of 2026-11-15). The release's target is the head of the CI run that
# triggered it, and a merge that touches the workflows can land on main while
# that run is still going.
#
# Exit 0: the target is main's head, or it is behind and the workflows tree is
#   identical — the cut can go on. One line says which.
# Exit 10: the target is behind main AND .github/workflows differs. Nothing
#   can be tagged on it; the later push run covers the same range, because the
#   release base advances only when a tag is cut. A notice, never an error.
# Exit 2: a ref cannot be resolved — fail closed, never read as "fresh".
# Exit 64: bad usage.
set -uo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: check-target-fresh.sh <target-sha> [<main-ref>]" >&2
  exit 64
fi
target="$1"
main_ref="${2:-origin/main}"

target_sha="$(git rev-parse --verify --quiet "${target}^{commit}")" || {
  echo "check-target-fresh: cannot resolve the target '$target'" >&2
  exit 2
}
main_sha="$(git rev-parse --verify --quiet "${main_ref}^{commit}")" || {
  echo "check-target-fresh: cannot resolve '$main_ref'" >&2
  exit 2
}

if [ "$target_sha" = "$main_sha" ]; then
  echo "check-target-fresh: the target ${target_sha:0:12} is the head of $main_ref"
  exit 0
fi

git diff --quiet "$target_sha" "$main_sha" -- .github/workflows
case "$?" in
  0)
    echo "check-target-fresh: the target ${target_sha:0:12} is behind $main_ref (${main_sha:0:12}) but .github/workflows is the same: the cut can go on"
    exit 0
    ;;
  1)
    echo "check-target-fresh: the target ${target_sha:0:12} is behind $main_ref (${main_sha:0:12}) and .github/workflows differs between them: GitHub would refuse every tag as a workflow update by an App without the workflows permission. Nothing is cut; the release of the later push run covers the same range"
    exit 10
    ;;
  *)
    echo "check-target-fresh: cannot compare .github/workflows of $target_sha and $main_ref" >&2
    exit 2
    ;;
esac
