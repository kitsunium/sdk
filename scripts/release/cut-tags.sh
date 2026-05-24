#!/usr/bin/env bash
# scripts/release/cut-tags.sh — given a list of majors on stdin (one per
# line, from compute-bumps.sh), compute the next patch tag for each,
# strip `replace` directives from a temp go.mod copy, verify the module
# graph resolves under GOWORK=off, then atomically push the tag.
#
# Race-protected (plan B3): re-reads the latest tag immediately before
# the push and aborts on drift.

set -euo pipefail
shopt -s nullglob

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"

DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --help|-h)
      cat <<EOF
cut-tags.sh — read majors (vN) on stdin, push next patch tag per major.

Usage: $0 [--dry-run] < majors.txt

Honors a 'Release-bump: minor' trailer on the latest merge commit, but
only if that commit actually touches pkg/<major>/.
EOF
      exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 64 ;;
  esac
done

# Inspect the *merge commit* (HEAD on main is the squash/merge commit
# in the standard PR flow) and parse its Release-bump trailer once.
trailer_raw="$(git log -1 --format='%(trailers:key=Release-bump,valueonly,separator=,)' HEAD 2>/dev/null || true)"
# Files actually touched by that commit — used to scope the trailer
# (plan B4: prevents an attacker from gaming v1's version by adding a
# trailer in a commit that only touches v2's tree).
merge_touched_paths="$(git log -1 --name-only --format= HEAD 2>/dev/null || true)"

# Pre-release tags refuse to bump (plan B7). The shared lib already
# rejects them inside next_patch / next_minor — surface a clear message.
bump_for_major() {
  local major="$1" last="$2" trailer
  trailer="$(grep -F "/$major/" <<<"$merge_touched_paths" >/dev/null 2>&1 && echo "$trailer_raw" || echo)"
  case "$trailer" in
    minor) next_minor "$last" ;;
    major) echo "cut-tags: 'Release-bump: major' rejected — create pkg/v$(( ${major#v} + 1 ))/" >&2; return 1 ;;
    *)     next_patch "$last" ;;
  esac
}

verify_module_graph() {
  # Two statements: `local major="$1" modroot="pkg/$major"` evaluates modroot
  # against the OUTER (empty) major, yielding "pkg/" (shellcheck SC2318).
  local major="$1"
  local modroot="pkg/$major"
  [ -d "$modroot" ] || { echo "cut-tags: $modroot missing" >&2; return 1; }
  # Run in a SUBSHELL so the cleanup trap is scoped to it (fires on subshell
  # EXIT). A function-level `trap ... RETURN` would also fire on every later
  # function return (e.g. latest_tag_for) with an out-of-scope $tmp and abort
  # the script under set -eu.
  (
    tmp="$(mktemp -d)"
    trap 'rm -rf -- "$tmp"' EXIT
    # Strip single-line AND block-form `replace (...)` from a copy of go.mod
    # (a line-based grep leaves the block body + a dangling `)`, producing an
    # invalid go.mod), then verify the bare graph resolves under GOWORK=off
    # (plan B9, B10).
    awk '
      /^[[:space:]]*replace[[:space:]]*\(/ { inblk = 1; next }
      inblk && /^[[:space:]]*\)/           { inblk = 0; next }
      inblk                                { next }
      /^[[:space:]]*replace[[:space:]]+/   { next }
      { print }
    ' "$modroot/go.mod" > "$tmp/go.mod"
    cp "$modroot/go.sum" "$tmp/go.sum" 2>/dev/null || true
    cd "$tmp" && GOWORK=off go mod download all >/dev/null 2>&1
  ) || {
    echo "cut-tags: go mod download failed for $modroot (GOWORK=off, replace stripped)" >&2
    return 1
  }
}

while IFS= read -r major; do
  [ -z "$major" ] && continue
  case "$major" in v[0-9]*) ;; *) echo "cut-tags: bad major '$major'" >&2; exit 1 ;; esac

  last="$(latest_tag_for "$major" || true)"
  if [ -z "$last" ]; then
    # First release for this major: seed the major-aligned v<N>.0.0 directly
    # (e.g. v2 → pkg/v2/v2.0.0). Faking last="pkg/${major}/v1.0.0" for every
    # major produced invalid lineage like pkg/v2/v1.0.1 (semver major ≠ path
    # major). `$major` already carries the leading "v", so "${major}.0.0".
    next="pkg/${major}/${major}.0.0"
    if ! is_valid_tag "$next"; then
      echo "cut-tags: computed invalid first tag '$next' for $major" >&2
      exit 1
    fi
  else
    if ! is_valid_tag "$last"; then
      echo "cut-tags: refusing pre-release or malformed: $last" >&2
      exit 1
    fi
    next="$(bump_for_major "$major" "$last")"
  fi

  verify_module_graph "$major"

  # Race window close (plan B3): re-read latest tag immediately before
  # the push and abort if another job tagged in between.
  latest_now="$(latest_tag_for "$major" || true)"
  if [ -n "$latest_now" ] && [ "$latest_now" != "$last" ]; then
    echo "cut-tags: race detected — $major moved from $last to $latest_now during prep; aborting" >&2
    exit 2
  fi

  if [ "$DRY_RUN" -eq 1 ]; then
    echo "DRY-RUN: would tag $next (from $last)"
    continue
  fi

  git tag -a "$next" -m "release $next"
  git push --atomic origin "$next"
  echo "$next"
done
