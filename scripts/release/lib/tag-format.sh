#!/usr/bin/env bash
# scripts/release/lib/tag-format.sh — single source of truth for the SDK
# release tag format. Sourced by compute-bumps.sh + cut-tags.sh; the JS
# counterpart at docs/site/scripts/lib/tag-format.mjs re-exports the same
# regex so the docs sync stays in sync with the release pipeline.
#
# Tag shape (load-bearing — also enforced in ADR 0007):
#     pkg/<major>/v<X>.<Y>.<Z>(-<prerelease>)?
# Example: pkg/v1/v1.32.1   pkg/v1/v1.32.0-rc.1   pkg/v2/v2.0.0

set -euo pipefail

# Public regex used by every consumer. Anchored.
TAG_REGEX='^pkg/v[0-9]+/v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# Test a tag name against the canonical shape. Exits 0 on match.
is_valid_tag() {
  [[ "$1" =~ $TAG_REGEX ]] || return 1
  # Path major (pkg/vN) MUST equal the semver major (vN.…). The regex alone
  # accepts mismatches like pkg/v2/v1.2.3, which break release invariants
  # (ADR 0007 §1). Kept in lockstep with tag-format.mjs's parseTag.
  local path_major ver_major
  path_major="$(awk -F/ '{sub(/^v/, "", $2); print $2}' <<<"$1")"
  ver_major="$(awk -F/ '{split($3, a, "."); sub(/^v/, "", a[1]); print a[1]}' <<<"$1")"
  [[ "$path_major" == "$ver_major" ]]
}

# Extract the "vN" major from a full tag. Echo only; never exits non-zero
# on bad input — caller must gate with is_valid_tag first.
major_from_tag() {
  awk -F/ '{print $2}' <<<"$1"
}

# Extract the bare semver X.Y.Z(-rc)? from a full tag.
version_from_tag() {
  awk -F/ '{sub(/^v/, "", $3); print $3}' <<<"$1"
}

# Strip a pre-release suffix from a semver. "1.0.0-rc.1" -> "1.0.0".
strip_prerelease() {
  sed 's/-.*$//' <<<"$1"
}

# Bump the patch component of a tag. Refuses pre-release tags (the
# release pipeline must not auto-bump from a pre-release base).
next_patch() {
  local tag="$1"
  is_valid_tag "$tag" || { echo "next_patch: invalid tag '$tag'" >&2; return 1; }
  local ver
  ver="$(version_from_tag "$tag")"
  case "$ver" in
    *-*) echo "next_patch: refusing to bump from pre-release '$tag'" >&2; return 1 ;;
  esac
  local major minor patch
  IFS=. read -r major minor patch <<<"$ver"
  printf 'pkg/%s/v%s.%s.%s\n' "$(major_from_tag "$tag")" "$major" "$minor" "$((patch + 1))"
}

# Bump the minor component (resets patch to 0). Same pre-release guard.
next_minor() {
  local tag="$1"
  is_valid_tag "$tag" || { echo "next_minor: invalid tag '$tag'" >&2; return 1; }
  local ver
  ver="$(version_from_tag "$tag")"
  case "$ver" in
    *-*) echo "next_minor: refusing to bump from pre-release '$tag'" >&2; return 1 ;;
  esac
  local major minor _patch
  IFS=. read -r major minor _patch <<<"$ver"
  printf 'pkg/%s/v%s.%s.0\n' "$(major_from_tag "$tag")" "$major" "$((minor + 1))"
}

# Cross-platform version sort. GNU `sort -V` ships on Linux; BSD `sort`
# (default on macOS) doesn't. Autodetect once per process.
version_sort() {
  if sort -V </dev/null >/dev/null 2>&1; then
    sort -V "$@"
  else
    # Fallback: lexicographic on the dotted-quad after stripping the
    # "pkg/vN/v" prefix. Good enough for X.Y.Z up to 99999.
    sort -t. -k1,1n -k2,2n -k3,3n "$@"
  fi
}

# Find the highest valid tag for a major ("v1"). Echoes empty if none.
latest_tag_for() {
  local major="$1"
  git tag -l "pkg/${major}/v*" |
    while IFS= read -r t; do is_valid_tag "$t" && echo "$t"; done |
    version_sort |
    tail -n1
}
