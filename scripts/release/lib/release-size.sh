#!/usr/bin/env bash
# scripts/release/lib/release-size.sh — HOW BIG a release is, decided from what a
# maintainer set on the merged pull request, never from text a merge composed
# (ADR 0135). Sourced by cut-tags.sh, which sizes a release after the merge, and
# by check-pr-size.sh, which asks the same question of a pull request before it.
# One decision in one place, because the two used to be one script and a copy.
#
# Why the channel moved. The size used to be a `Release-bump:` trailer in the
# first-parent commit message, and this repository squash-merges with
# `squash_merge_commit_message: COMMIT_MESSAGES`: GitHub composes that message
# from the BRANCH commits, which is contributor text. Two defects followed from
# the one fact, in opposite directions:
#
#   - contributor text BURIED the maintainer's trailer. `%(trailers:…)` reads
#     the last paragraph only, so a trailer followed by a folded branch commit
#     was invisible: pkg/v0.1.35 and pkg/v0.3.4 shipped as patches that had
#     asked for a minor (#217). ADR 0089's refusal turned that into a stop —
#     and the stop is what blocked the release of ce9323c (#248): the
#     `Release-bump: minor` of one branch commit landed mid-body, the job
#     exited 65, and the only way past was cutting the tags by hand with
#     `cut-tags.sh --range` (#224);
#   - contributor text SET the size. A message whose last paragraph is a
#     contributor's `Release-bump: minor` was honoured exactly like a
#     maintainer's, because nothing in the message says who wrote a line (#224).
#
# A label on the pull request has neither property. GitHub lets only an account
# with triage or write access apply one, so a contributor without that access
# cannot size a release; and it is structured data, so it has no last
# paragraph, no neighbour that breaks a block and no separator to mis-parse.
#
# The text is still READ, for one reason: a maintainer who writes
# `Release-bump: minor` and forgets the label must not get a patch in silence —
# that is #217 again, by another route. So a message asking for MORE than
# `patch` with no label to decide it is refused, loudly, before anything is
# published; a label, when present, decides, and the text is only reported.
#
# No `grep -q`, no `grep -c`, no pipeline into an early-exiting reader: the
# SIGPIPE shape has cost this repository a release twice (ADR 0085, and the
# comment above compute-bumps.sh's rdeps query). Everything here reads to EOF.

set -euo pipefail

# size_rank <size> — patch 0, minor 1, major 2. Exits 1 on anything else, so a
# caller can tell "not a size" from "the smallest size".
size_rank() {
  case "$1" in
    patch) echo 0 ;;
    minor) echo 1 ;;
    major) echo 2 ;;
    *) return 1 ;;
  esac
}

# text_ask — stdin: one commit message. stdout: the largest size a column-0
# `Release-bump:` line asks for, or nothing.
#
# Anywhere in the message, not only the last paragraph: the position of the line
# no longer decides anything, which is the point — a request buried by a folded
# branch commit is still a request. A value that is not a size is ignored, as it
# always was ("an unreadable trailer must never raise a release"), and so is
# `patch`, because asking for the default is not asking for anything.
#
# Default IFS on the read, so `Release-bump: minor ` written with a stray space
# still ranks as minor. Two lines spelling halves of a word (`mi`, `nor`) are two
# values that are not sizes — they never concatenate, whatever the separator.
text_ask() {
  local line value best="" best_rank=0 r
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "Release-bump:"*)
        value="${line#Release-bump:}"
        read -r value _ <<<"$value" || true
        r="$(size_rank "$value")" || continue
        if [ "$r" -gt "$best_rank" ]; then
          best_rank="$r"
          best="$value"
        fi
        ;;
    esac
  done
  if [ -n "$best" ]; then echo "$best"; fi
}

# label_size — stdin: label names, one per line. stdout: the size the `release:*`
# label among them names, or nothing when there is none.
#
# Refused (exit 1, reason on stderr) when two DIFFERENT release labels are set,
# because either answer would be a guess about which one the maintainer meant;
# and when a `release:` label names something that is not a size, because a
# typo'd label that silently reads as "no label" is a minor published as a
# patch. Compared lower-case: GitHub treats label names case-insensitively.
label_size() {
  local name lower size="" r
  while IFS= read -r name || [ -n "$name" ]; do
    lower="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
    case "$lower" in
      release:*)
        r="${lower#release:}"
        if ! size_rank "$r" >/dev/null; then
          echo "the label '$name' is not a release size (release:patch, release:minor or release:major)" >&2
          return 1
        fi
        if [ -n "$size" ] && [ "$size" != "$r" ]; then
          echo "two release labels, release:$size and release:$r — keep the one that is meant" >&2
          return 1
        fi
        size="$r"
        ;;
    esac
  done
  if [ -n "$size" ]; then echo "$size"; fi
}

# release_gh — the GitHub CLI every lookup goes through. Overridable with
# RELEASE_GH so a suite can prove what an ABSENT one does: the hosted Ubuntu
# runners ship gh in /usr/bin, which a PATH edit cannot take away.
release_gh() {
  "${RELEASE_GH:-gh}" "$@"
}

# release_repo — the owner/repo the lookups address: RELEASE_REPO when set, the
# workflow's GITHUB_REPOSITORY in Actions, else gh's own `{owner}/{repo}`
# placeholder, which it resolves from the clone's remote.
release_repo() {
  if [ -n "${RELEASE_REPO:-}" ]; then
    echo "$RELEASE_REPO"
  elif [ -n "${GITHUB_REPOSITORY:-}" ]; then
    echo "$GITHUB_REPOSITORY"
  else
    echo '{owner}/{repo}'
  fi
}

# pr_labels_of_commit <sha> — what GitHub knows about the merge that put <sha>
# on the branch. stdout: nothing when no merged pull request introduced it (a
# direct push); otherwise a first line naming the pull request(s) — `#248` —
# then one label name per line.
#
# Exit 1 when GitHub could not answer. An API that failed is not a commit
# without a pull request: reading the one as the other would size a release
# from an absence of measurement, which is the defect class #226 and #227 are
# about. The caller refuses.
#
# `merged_at != null` rather than `merge_commit_sha == <sha>`: the endpoint
# answers "the merged pull request that introduced the commit", and a
# rebase-merge introduces every commit of the branch while recording only one
# of them as its merge commit. Every pull request it names contributes its
# labels; two that disagree are refused by label_size like two labels on one.
pr_labels_of_commit() {
  local sha="$1" out="" rc=0
  case "$sha" in
    "" | *[!0-9a-f]*)
      echo "not a commit id: '$sha'" >&2
      return 1
      ;;
  esac
  if ! command -v "${RELEASE_GH:-gh}" >/dev/null 2>&1; then
    echo "'${RELEASE_GH:-gh}' is not on PATH, so the pull request that introduced $sha cannot be read" >&2
    return 1
  fi
  out="$(release_gh api "repos/$(release_repo)/commits/${sha}/pulls" \
    --jq '[.[] | select(.merged_at != null)] | if length == 0 then empty else ((map("#" + (.number | tostring)) | join(",")), (map(.labels[].name) | unique | .[])) end')" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "GitHub did not answer for $sha (gh exit $rc)" >&2
    return 1
  fi
  if [ -n "$out" ]; then printf '%s\n' "$out"; fi
}

# decide_size <label-size> <text-ask> — stdout: the size one merged change
# carries. The whole rule, in the order it is applied:
#
#   1. a release label decides — in either direction. `release:patch` over a
#      contributor's `Release-bump: minor` is how a maintainer declines a size
#      nobody with the authority asked for; that is the vector #224 is about.
#   2. no label, and the text asks for more than a patch: REFUSED (exit 1).
#      Publishing the patch would lose a request somebody may have meant (#217);
#      honouring the text would let a contributor size a release (#224). Only a
#      maintainer can settle it, and every way to do so is written in the
#      refusal.
#   3. no label and no request: a patch, the default ADR 0007 §2 always had.
decide_size() {
  local label="$1" ask="$2"
  if [ -n "$label" ]; then
    echo "$label"
    return 0
  fi
  if [ -n "$ask" ]; then
    return 1
  fi
  echo "patch"
}
