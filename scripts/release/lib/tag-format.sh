#!/usr/bin/env bash
# scripts/release/lib/tag-format.sh — single source of truth for the SDK
# release tag format. Sourced by compute-bumps.sh + cut-tags.sh; the JS
# counterpart at docs/site/scripts/lib/tag-format.mjs mirrors the same shapes
# so the docs sync stays in step with the release pipeline (ADR 0007 §1).
#
# The SDK is ONE Go module, github.com/kitsunium/sdk, at the repository root
# (ADR 0162). A release is one tag on it:
#
#     v<X>.<Y>.<Z>(-<prerelease>)?          e.g. v0.18.0   v1.0.0-rc.1
#
# and, for each module that requires a vendor and CHANGED since its own last
# tag, one tag at the same version:
#
#     third-party/<path>/v<X>.<Y>.<Z>       e.g. third-party/aws/v0.18.0
#     framework/connectors/<engine>/v<X>.<Y>.<Z>
#
# The module paths are bare (no /vN suffix), so the semver major is held to
# 0|1 in every shape: a breaking v2 needs a real `…/v2` module path (deferred —
# ADR 0009). The shapes before ADR 0162 — `pkg/vX.Y.Z`, `internal/<mod>/vX.Y.Z`,
# `framework/vX.Y.Z` — are history: nothing cuts them any more, and the newest
# `pkg/vX.Y.Z` is read once, as the version the first root tag continues.

set -euo pipefail

# The SDK module's tag: the repository root, so no path prefix. Anchored, major
# 0|1 (bare module path — see header).
SDK_TAG_REGEX='^v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# is_valid_tag <tag> — the SDK module's shape. Exits 0 on match.
is_valid_tag() {
  [[ "$1" =~ $SDK_TAG_REGEX ]]
}

# The public module's tag before ADR 0162 (ADR 0017): `pkg/vX.Y.Z`. Kept only
# to READ the history — the first root tag continues pkg's numbering.
PKG_TAG_REGEX='^pkg/v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# is_valid_pkg_tag <tag> — a public-module tag from before ADR 0162. Exits 0 on
# match.
is_valid_pkg_tag() {
  [[ "$1" =~ $PKG_TAG_REGEX ]]
}

# Vendor-module tags (ADR 0157): `third-party/<path>/vX.Y.Z`, one per vendor
# module — `third-party/aws/v0.18.0`, `third-party/db/writer/mysql/v0.18.0`. A
# path component is lowercase and may carry a hyphen (`x-crypto`), as the
# directory does; at least one component is required, so a bare
# `third-party/vX.Y.Z` is no module's tag.
THIRD_PARTY_TAG_REGEX='^third-party(/[a-z][a-z0-9-]*)+/v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# is_valid_third_party_tag <tag> — a vendor module's shape. Exits 0 on match.
is_valid_third_party_tag() {
  [[ "$1" =~ $THIRD_PARTY_TAG_REGEX ]]
}

# Connector-module tags (ADR 0147 §7, ADR 0158): one driver each,
# `framework/connectors/<engine>/vX.Y.Z`. The framework itself is a part of the
# SDK module since ADR 0162, so `framework/vX.Y.Z` is no longer a shape.
CONNECTOR_TAG_REGEX='^framework/connectors/[a-z][a-z0-9]*/v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# is_valid_connector_tag <tag> — a connector module's shape. Exits 0 on match.
is_valid_connector_tag() {
  [[ "$1" =~ $CONNECTOR_TAG_REGEX ]]
}

# is_valid_vendor_tag <tag> — one of the two shapes a module that requires a
# vendor is tagged with.
is_valid_vendor_tag() {
  is_valid_third_party_tag "$1" || is_valid_connector_tag "$1"
}

# is_valid_release_tag <tag> — any tag a release cuts: the SDK's or a vendor
# module's.
is_valid_release_tag() {
  is_valid_tag "$1" || is_valid_vendor_tag "$1"
}

# is_vendor_dir <dir> — a directory that may hold a module requiring a vendor:
# `third-party/<path>` or `framework/connectors/<engine>`, spelled as its tag
# prefix must be.
is_vendor_dir() {
  is_valid_vendor_tag "$1/v0.0.0"
}

# vendor_modules [go.work] — the modules a release may tag beside the SDK, one
# directory per line, sorted: the workspace's `use` directives with the SDK
# module (`.`) left out. Deriving the list from go.work is what makes a module
# added to the workspace released without anyone editing this file — ADR
# 0137's census argument, applied to tags (ADR 0147 §9).
#
# It REFUSES rather than guesses: a missing go.work, one it cannot read, one
# that does not use `.` — the SDK module itself —, or one that uses a directory
# that is no vendor module is an exit 1 with the reason on stderr. The last is
# the guard on ADR 0162 itself: `./pkg`, `./framework` or `./internal/core`
# back in go.work would be a module split the release must not tag, since its
# packages are the SDK module's too, and a consumer requiring both would get an
# ambiguous import.
#
# Both `use ./x` and a parenthesised `use ( ... )` block are read; a `//`
# comment and blank lines are skipped.
vendor_modules() {
  local work="${1:-go.work}" dirs="" rc=0 d bad=""
  if [ ! -r "$work" ]; then
    echo "vendor_modules: $work is missing or unreadable — refusing to guess the release's modules" >&2
    return 1
  fi
  dirs="$(awk '
    { sub(/\/\/.*/, "") }
    /^[[:space:]]*use[[:space:]]*\(/      { inblock = 1; next }
    inblock && /^[[:space:]]*\)/          { inblock = 0; next }
    inblock && NF                          { print $1; next }
    /^[[:space:]]*use[[:space:]]+[^([:space:]]/ { print $2 }
  ' "$work" | sed -e 's#^\./##' -e 's#/$##')" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "vendor_modules: could not parse $work (exit $rc)" >&2
    return 1
  fi
  if ! grep -qx '\.' <<<"$dirs"; then
    echo "vendor_modules: $work does not use . — refusing a release without the SDK module (ADR 0162)" >&2
    return 1
  fi
  while IFS= read -r d; do
    [ -z "$d" ] && continue
    [ "$d" = "." ] && continue
    if ! is_vendor_dir "$d"; then
      bad="${bad:+$bad }$d"
    fi
  done <<<"$dirs"
  if [ -n "$bad" ]; then
    echo "vendor_modules: $work uses $bad, which is no vendor module — the SDK is one module (ADR 0162), and a module of its own lives under third-party/ or framework/connectors/" >&2
    return 1
  fi
  grep -vx '\.' <<<"$dirs" | LC_ALL=C sort -u || true
}

# version_from_tag <tag> — the bare semver X.Y.Z(-rc)? of any shape:
# "v0.1.0" -> "0.1.0", "pkg/v0.1.0" -> "0.1.0",
# "third-party/aws/v0.1.0" -> "0.1.0".
version_from_tag() {
  sed -e 's#^.*/##' -e 's#^v##' <<<"$1"
}

# Strip a pre-release suffix from a semver. "1.0.0-rc.1" -> "1.0.0".
strip_prerelease() {
  sed 's/-.*$//' <<<"$1"
}

# is_valid_base <tag> — a tag a version may be bumped FROM: the SDK's, or —
# only until the first root tag exists — pkg's.
is_valid_base() {
  is_valid_tag "$1" || is_valid_pkg_tag "$1"
}

# next_patch <tag> — the next SDK tag, patch bumped, from an SDK or a pkg base:
# "v0.1.0" -> "v0.1.1", "pkg/v0.17.0" -> "v0.17.1". Refuses a pre-release base
# (the release pipeline must not auto-bump from one).
next_patch() {
  local tag="$1"
  is_valid_base "$tag" || { echo "next_patch: invalid tag '$tag'" >&2; return 1; }
  local ver
  ver="$(version_from_tag "$tag")"
  case "$ver" in
    *-*) echo "next_patch: refusing to bump from pre-release '$tag'" >&2; return 1 ;;
  esac
  local major minor patch
  IFS=. read -r major minor patch <<<"$ver"
  printf 'v%s.%s.%s\n' "$major" "$minor" "$((patch + 1))"
}

# next_minor <tag> — the next SDK tag, minor bumped and patch reset, from an SDK
# or a pkg base: "pkg/v0.17.0" -> "v0.18.0". Same pre-release guard.
next_minor() {
  local tag="$1"
  is_valid_base "$tag" || { echo "next_minor: invalid tag '$tag'" >&2; return 1; }
  local ver
  ver="$(version_from_tag "$tag")"
  case "$ver" in
    *-*) echo "next_minor: refusing to bump from pre-release '$tag'" >&2; return 1 ;;
  esac
  local major minor _patch
  IFS=. read -r major minor _patch <<<"$ver"
  printf 'v%s.%s.0\n' "$major" "$((minor + 1))"
}

# version_sort — stdin: tags of any shape, one per line. stdout: the same tags,
# ascending by their X.Y.Z, whatever their prefix. Decorate, sort numerically,
# undecorate: GNU `sort -V` is not on every macOS, and the fallback this
# replaces sorted on a fixed `pkg/v` prefix, which the root shape does not
# carry.
version_sort() {
  awk '{
    v = $0
    sub(/^.*\//, "", v)
    sub(/^v/, "", v)
    sub(/-.*$/, "", v)
    split(v, p, ".")
    printf "%d\t%d\t%d\t%s\n", p[1], p[2], p[3], $0
  }' | LC_ALL=C sort -t "$(printf '\t')" -k1,1n -k2,2n -k3,3n | cut -f4-
}

# stable_tags <glob> <validator> — every tag `git tag -l` lists for <glob> that
# <validator> accepts and whose VERSION carries no pre-release suffix, one per
# line. Pre-release tags are left out: they are not a valid bump base
# (next_patch and next_minor refuse them), and a sort on X.Y.Z alone would tie
# v0.2.0 with v0.2.0-rc.1. The hyphen is looked for in the version only:
# `third-party/x-crypto/v0.18.0` is stable, and its path carries two.
stable_tags() {
  local glob="$1" check="$2"
  git tag -l "$glob" |
    while IFS= read -r t; do
      "$check" "$t" || continue
      case "$(version_from_tag "$t")" in *-*) continue ;; esac
      echo "$t"
    done
}

# latest_sdk_tag — the highest stable SDK tag `vX.Y.Z`; empty when the root
# module has never been released.
latest_sdk_tag() {
  stable_tags 'v*' is_valid_tag | version_sort | tail -n1
}

# latest_pkg_tag — the highest stable `pkg/vX.Y.Z` from before ADR 0162; empty
# if none.
latest_pkg_tag() {
  stable_tags 'pkg/v*' is_valid_pkg_tag | version_sort | tail -n1
}

# latest_release_tag — the tag the last release was cut as: the higher of the
# newest SDK tag and the newest pkg tag. Until the first SDK tag exists that is
# pkg's, whose release cut the chain the SDK module replaced; after it, the SDK
# tag continues pkg's numbering, so it is always the higher. Comparing the two
# rather than preferring one is what keeps a stray low root tag from restarting
# the numbering below the history. Empty on a repository that never released.
latest_release_tag() {
  { latest_sdk_tag; latest_pkg_tag; } | awk 'NF' | version_sort | tail -n1
}

# latest_vendor_tag <dir> — the highest stable tag of the module in <dir>
# (`<dir>/vX.Y.Z`); empty when it has never been tagged. A tag whose prefix is
# not exactly <dir> is not the module's, whatever the glob matched.
latest_vendor_tag() {
  local dir="$1"
  stable_tags "$dir/v*" is_valid_vendor_tag |
    while IFS= read -r t; do
      if [ "${t%/v*}" = "$dir" ]; then echo "$t"; fi
    done |
    version_sort |
    tail -n1
}

# The commit the last release was cut FROM — the baseline that BOTH halves of a
# release measure against. Echoes nothing when there is no baseline at all (a
# bootstrap repo whose only commit is HEAD); each caller renders that in its own
# terms, because "everything" is an empty tree to `git diff` and the whole
# history to `git log`.
#
# Why the tag's FIRST PARENT rather than the tag itself: cut-tags.sh publishes
# every release on a DETACHED commit that is a child of the main commit it was
# cut from (ADR 0009 — the dev branch is never touched). That release commit is
# not reachable from HEAD, so `git describe --tags` cannot see it and baselining
# on the tag would compare HEAD against something outside its own history.
#
# The tag is latest_release_tag's: the newest SDK tag or, before the first one,
# the newest pkg tag — the release the first root tag follows was cut as pkg's,
# and the range must not reopen what it published (ADR 0162).
#
# Why this lives in the shared lib and not in compute-bumps.sh, where it was
# born: the two halves of a release have to agree on it. compute-bumps.sh
# decides WHETHER to release by diffing this baseline against HEAD; cut-tags.sh
# decides HOW BIG by reading the maintainer's labels over the same range. While
# cut-tags.sh read HEAD alone the two could disagree — and when they did, the
# bump fell silently to patch (ADR 0085). One function is what keeps them
# symmetric; two copies would drift the same way again.
#
# No JS counterpart in docs/site/scripts/lib/tag-format.mjs: that file mirrors
# the tag SHAPE, and the docs sync reads published GitHub releases, never the
# commit graph.
release_base() {
  local last="" last_base=""
  last="$(latest_release_tag 2>/dev/null || true)"
  if [ -n "$last" ]; then
    last_base="$(git rev-parse -q --verify "${last}^1^{commit}" 2>/dev/null || true)"
    if [ -n "$last_base" ]; then
      echo "$last_base"
      return 0
    fi
  fi
  # No usable release tag. A repo with 0 or 1 commits has no "before" to point
  # at — root..HEAD would be empty and hide the initial commit entirely — so
  # echo nothing and let the caller decide what covering everything means.
  if [ "$(git rev-list --count HEAD 2>/dev/null || echo 0)" -le 1 ]; then
    return 0
  fi
  git rev-list --max-parents=0 HEAD | head -n1
}

# tag_base <tag> — the commit <tag> was cut FROM: its first parent, for the
# reason release_base gives. A vendor module's change is measured from there,
# never from the tag, whose go.mod the release rewrote. Exit 1 when the tag
# names no commit with a parent.
tag_base() {
  git rev-parse -q --verify "${1}^1^{commit}"
}
