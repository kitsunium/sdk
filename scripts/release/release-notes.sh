#!/usr/bin/env bash
# scripts/release/release-notes.sh — what sdk-release.yml says about the tags a
# release pushed: which family each belongs to, and the notes of the one GitHub
# release. Read from cut-tags.sh's stdout, the tag list, one per line.
#
# It used to be three inline awk programs in the workflow, each classifying a
# tag with `/^(pkg|framework)\/v/` — unanchored at the end, so every tag of a
# module under pkg/ (`pkg/v1/data/v0.1.0`) read as a tombstone and its notes
# said a package-holding module "holds no package". The classification now goes
# through lib/tag-format.sh's ANCHORED regexes, the ones cut-tags.sh validated
# the tags against before it pushed them, so the notes cannot disagree with the
# release about what a tag is. A tag none of them accepts is refused: the notes
# of a release must not describe a tag the release does not know.
#
# Usage:
#   release-notes.sh classify <tag-list>   one "<kind> <tag>" line per tag
#   release-notes.sh count <tag-list>      "<kind>=<n>" for every kind
#   release-notes.sh notes <tag-list>      the release notes, Markdown
#
# Exit 1 on a tag no family accepts, or on an empty or unreadable list; 64 on
# bad usage.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"

SDK_MODULE="github.com/kitsunium/sdk"

# tag_kind <tag> — the family of one pushed tag, by the anchored shapes alone.
# The tombstone is tested first: `pkg/vX.Y.Z` is no vendor shape anyway, and
# the order says which question settles it.
tag_kind() {
  if is_valid_tombstone_tag "$1"; then
    echo tombstone
  elif is_valid_tag "$1"; then
    echo sdk
  elif is_valid_module_tag "$1"; then
    echo module
  elif is_valid_vendor_tag "$1"; then
    echo vendor
  else
    return 1
  fi
}

# classify <file> — "<kind> <tag>" per non-empty line; refuses an unknown tag
# and an empty list.
classify() {
  local file="$1" t k n=0
  if [ ! -r "$file" ]; then
    echo "release-notes: $file is missing or unreadable" >&2
    return 1
  fi
  while IFS= read -r t || [ -n "$t" ]; do
    [ -z "$t" ] && continue
    if ! k="$(tag_kind "$t")"; then
      echo "release-notes: '$t' is no tag a release cuts — refusing to describe it" >&2
      return 1
    fi
    printf '%s %s\n' "$k" "$t"
    n=$((n + 1))
  done <"$file"
  if [ "$n" -eq 0 ]; then
    echo "release-notes: $file lists no tag" >&2
    return 1
  fi
}

count() {
  local out
  out="$(classify "$1")" || return 1
  awk '{ n[$1]++ } END { printf "sdk=%d module=%d vendor=%d tombstone=%d\n", n["sdk"], n["module"], n["vendor"], n["tombstone"] }' <<<"$out"
}

notes() {
  local out sdk modules vendors tombs t
  out="$(classify "$1")" || return 1
  sdk="$(awk '$1 == "sdk" { print $2 }' <<<"$out")"
  modules="$(awk '$1 == "module" { print $2 }' <<<"$out")"
  vendors="$(awk '$1 == "vendor" { print $2 }' <<<"$out")"
  tombs="$(awk '$1 == "tombstone" { print $2 }' <<<"$out")"
  echo "The SDK module \`$SDK_MODULE\` at \`$sdk\`."
  echo
  if [ -n "$modules" ]; then
    echo "Modules of the SDK's layout tagged by this release (ADR 0170):"
    echo
    while IFS= read -r t; do
      echo "- \`$t\` — \`$SDK_MODULE/${t%/v*}\`"
    done <<<"$modules"
    echo
  fi
  if [ -n "$vendors" ]; then
    echo "Modules that require a vendor, tagged by this release because they changed:"
    echo
    while IFS= read -r t; do
      echo "- \`$t\` — \`$SDK_MODULE/${t%/v*}\`"
    done <<<"$vendors"
  else
    echo "No module that requires a vendor changed: each keeps its last tag."
  fi
  if [ -n "$tombs" ]; then
    echo
    echo "The modules the SDK module replaced get their last version, a go.mod that requires \`$SDK_MODULE\` \`$sdk\` and holds no package, so an import of their paths resolves to the SDK module (ADR 0162):"
    echo
    while IFS= read -r t; do
      echo "- \`$t\` — \`$SDK_MODULE/${t%/v*}\`"
    done <<<"$tombs"
  fi
}

if [ "$#" -ne 2 ]; then
  echo "usage: $0 classify|count|notes <tag-list>" >&2
  exit 64
fi
case "$1" in
  classify) classify "$2" ;;
  count) count "$2" ;;
  notes) notes "$2" ;;
  *)
    echo "usage: $0 classify|count|notes <tag-list>" >&2
    exit 64
    ;;
esac
