#!/usr/bin/env bash
# scripts/release/lib/tag-format.sh — single source of truth for the SDK
# release tag format. Sourced by compute-bumps.sh + cut-tags.sh; the JS
# counterpart at docs/site/scripts/lib/tag-format.mjs re-exports the same
# regex so the docs sync stays in sync with the release pipeline.
#
# Tag shape (load-bearing — also enforced in ADR 0007 / ADR 0009):
#     pkg/v<X>.<Y>.<Z>(-<prerelease>)?
# Example: pkg/v0.1.0   pkg/v0.2.0-rc.1   pkg/v1.0.0
#
# The public module is github.com/kitsunium/sdk/pkg — a BARE module path with
# NO /vN suffix. Go forbids /v0 and /v1 suffixes (v0/v1 are the suffix-free
# major; only /v2+ carry one), so the proxy rejects a `…/pkg/v1` module path at
# any version. The consumer-facing packages live under the pkg/v1/ directory
# (import paths stay github.com/kitsunium/sdk/pkg/v1/*) but the module — and its
# tag — is bare `pkg`. The semver major is therefore held to 0|1 here; a future
# breaking v2 adopts a real `…/pkg/v2` module path with a `pkg/v2/v2.0.0` tag
# (deferred — ADR 0009), at which point this lib grows a second shape.

set -euo pipefail

# Public regex used by every consumer. Anchored. Major constrained to 0|1
# (bare module path — see header); v2+ deferred.
TAG_REGEX='^pkg/v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# Test a tag name against the canonical shape. Exits 0 on match. The major
# bound (0|1) lives in the regex, so no path-major/semver-major cross-check is
# needed: the module path is bare and there is no path-major to disagree with.
is_valid_tag() {
  [[ "$1" =~ $TAG_REGEX ]]
}

# Internal-module resolution tags (ADR 0009): `internal/<mod>/vX.Y.Z`. These are
# cut alongside the pkg tag so the published module graph resolves without
# `replace`; Go's internal/ rule still blocks direct consumer import. Bare
# module paths only carry major 0/1, so the semver major is held to 0|1 (v2+
# would need internal/<mod>/vN paths — deferred per ADR 0009).
INTERNAL_TAG_REGEX='^internal/[a-z][a-z0-9]*/v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# Test an internal-module tag against the canonical shape. Exits 0 on match.
is_valid_internal_tag() {
  [[ "$1" =~ $INTERNAL_TAG_REGEX ]]
}

# Framework-module tags (ADR 0143): `framework/vX.Y.Z` for the framework itself
# and `framework/connectors/<engine>/vX.Y.Z` for each database connector, a Go
# module of its own. Cut in lockstep with the pkg tag, same X.Y.Z, for the same
# reason the internal tags are: each requires the one below it EXACTLY.
FRAMEWORK_TAG_REGEX='^framework(/connectors/[a-z][a-z0-9]*)?/v[01]\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$'

# Test a framework-module tag against the canonical shape. Exits 0 on match.
is_valid_framework_tag() {
  [[ "$1" =~ $FRAMEWORK_TAG_REGEX ]]
}

# is_valid_chain_tag <tag> — one of the three shapes a release chain carries.
is_valid_chain_tag() {
  case "$1" in
    pkg/*) is_valid_tag "$1" ;;
    internal/*) is_valid_internal_tag "$1" ;;
    framework/*) is_valid_framework_tag "$1" ;;
    *) return 1 ;;
  esac
}

# chain_modules [go.work] — the modules a release tags, one directory per line,
# read from the workspace's `use` directives with the root module (`.`) left
# out: it is the umbrella that hosts third-party/ and nothing requires it
# (ADR 0012). Deriving the chain from go.work is what makes a module added to
# the workspace released without anyone editing this file — ADR 0137's census
# argument, applied to tags (ADR 0143 §9).
#
# It REFUSES rather than guesses: a missing go.work, one it cannot read, or one
# whose chain lacks `pkg` is an exit 1 with the reason on stderr, because a
# chain read as empty would publish nothing and one read without pkg would
# publish a framework requiring a pkg version that does not exist.
#
# Output order: internal/* first (by name — the tags are pushed atomically, so
# no order among them matters), then pkg, then framework, then connectors —
# the order each requires the one before, which is the order a reader of the
# dry-run expects. Both `use ./x` and a parenthesised `use ( ... )` block are
# read; a `//` comment and blank lines are skipped.
chain_modules() {
  local work="${1:-go.work}" dirs="" rc=0
  if [ ! -r "$work" ]; then
    echo "chain_modules: $work is missing or unreadable — refusing to guess the release chain" >&2
    return 1
  fi
  dirs="$(awk '
    { sub(/\/\/.*/, "") }
    /^[[:space:]]*use[[:space:]]*\(/      { inblock = 1; next }
    inblock && /^[[:space:]]*\)/          { inblock = 0; next }
    inblock && NF                          { print $1; next }
    /^[[:space:]]*use[[:space:]]+[^([:space:]]/ { print $2 }
  ' "$work" | sed -e 's#^\./##' -e 's#/$##' | awk '$0 != "." && $0 != ""')" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "chain_modules: could not parse $work (exit $rc)" >&2
    return 1
  fi
  if ! grep -qx 'pkg' <<<"$dirs"; then
    echo "chain_modules: $work does not use ./pkg — refusing a chain without the public module" >&2
    return 1
  fi
  {
    grep -E '^internal/' <<<"$dirs" | LC_ALL=C sort || true
    echo pkg
    grep -x 'framework' <<<"$dirs" || true
    grep -E '^framework/' <<<"$dirs" | LC_ALL=C sort || true
    grep -vE '^(internal/|pkg$|framework($|/))' <<<"$dirs" | LC_ALL=C sort || true
  }
}

# Extract the bare semver X.Y.Z(-rc)? from a pkg tag. "pkg/v0.1.0" -> "0.1.0".
version_from_tag() {
  awk -F/ '{sub(/^v/, "", $2); print $2}' <<<"$1"
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
  printf 'pkg/v%s.%s.%s\n' "$major" "$minor" "$((patch + 1))"
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
  printf 'pkg/v%s.%s.0\n' "$major" "$((minor + 1))"
}

# Cross-platform version sort. GNU `sort -V` ships on Linux; BSD `sort`
# (default on macOS) doesn't. Autodetect once per process.
version_sort() {
  if sort -V </dev/null >/dev/null 2>&1; then
    sort -V "$@"
  else
    # Fallback: numeric on the dotted-quad after the shared "pkg/v" prefix.
    # Good enough for X.Y.Z up to 99999.
    sort -t. -k1.6,1n -k2,2n -k3,3n "$@"
  fi
}

# Find the highest valid STABLE pkg tag. Echoes empty if none. Internal tags
# (internal/<mod>/v…) are excluded by the `pkg/v*` glob. Pre-release tags are
# skipped: they are not a valid bump base (next_patch/minor refuse them), and
# the BSD `version_sort` fallback only compares X.Y.Z — so pkg/v0.2.0 and
# pkg/v0.2.0-rc.1 would tie and `tail -n1` could hand back the rc.
latest_pkg_tag() {
  git tag -l 'pkg/v*' |
    while IFS= read -r t; do
      is_valid_tag "$t" || continue
      case "$t" in *-*) continue ;; esac
      echo "$t"
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
# Why this lives in the shared lib and not in compute-bumps.sh, where it was
# born: the two halves of a release have to agree on it. compute-bumps.sh
# decides WHETHER to release by diffing this baseline against HEAD; cut-tags.sh
# decides HOW BIG by reading the maintainer's Release-bump trailer. While
# cut-tags.sh read HEAD alone the two could disagree — and when they did, the
# bump fell silently to patch (ADR 0085). One function is what keeps them
# symmetric; two copies would drift the same way again.
#
# No JS counterpart in docs/site/scripts/lib/tag-format.mjs: that file mirrors
# the tag SHAPE, and the docs sync reads published GitHub releases, never the
# commit graph.
release_base() {
  local last_pkg="" last_base=""
  last_pkg="$(latest_pkg_tag 2>/dev/null || true)"
  if [ -n "$last_pkg" ]; then
    last_base="$(git rev-parse -q --verify "${last_pkg}^1^{commit}" 2>/dev/null || true)"
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
