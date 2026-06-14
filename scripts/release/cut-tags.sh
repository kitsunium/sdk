#!/usr/bin/env bash
# scripts/release/cut-tags.sh — read the bump token `pkg` on stdin (from
# compute-bumps.sh), compute the next patch tag, then publish a `go get`-able
# tag chain (ADR 0009): rewrite every chain module's go.mod to drop `replace`
# and pin its intra-repo deps to the release version, commit that on a detached
# release commit, and tag internal/{kernel,core,service} + pkg at the same
# version so a consumer resolves the whole graph from the proxy with no local
# context.
#
# The public module is the bare `github.com/kitsunium/sdk/pkg` (no /vN suffix —
# Go forbids it; the consumer packages live under the pkg/v1/ directory). Its
# tag is `pkg/vX.Y.Z` (major 0|1). See lib/tag-format.sh + ADR 0009.
#
# The dev branch is untouched — only the published tags carry the replace-free,
# cross-pinned go.mods. go.work + `replace` keep local dev working as before.
#
# Race-protected (plan B3): re-reads the latest tag immediately before the push
# and aborts on drift.
#
# go.sum + clean-room proxy resolution are finalised at the FIRST real release
# (ADR 0009): pushed-tag checksums cannot be computed before the tags exist, so
# this script produces the correct go.mod FORM and tags the chain; the first
# release validates `go get` from a clean machine and back-fills go.sum.

set -euo pipefail
shopt -s nullglob

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"

# Internal modules in pkg's publish chain (ADR 0001 fixes this set). They are
# tagged + cross-pinned at release so pkg resolves without `replace`; Go's
# internal/ rule still blocks direct consumer import — these tags exist only
# for module-graph resolution. Leaves first.
INTERNAL_MODULES=(internal/kernel internal/core internal/service)

DRY_RUN=0
ALLOW_BOOTSTRAP=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --allow-bootstrap) ALLOW_BOOTSTRAP=1 ;;
    --help | -h)
      cat <<EOF
cut-tags.sh — read the bump token 'pkg' on stdin, publish the next patch tag chain.

Usage: $0 [--dry-run] [--allow-bootstrap] < bump.txt

Honors a 'Release-bump: minor' trailer on the latest merge commit, but
only if that commit actually touches pkg/.
EOF
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 64
      ;;
  esac
done

# Inspect the *merge commit* (HEAD on main is the squash/merge commit in the
# standard PR flow) and parse its Release-bump trailer once.
trailer_raw="$(git log -1 --format='%(trailers:key=Release-bump,valueonly,separator=,)' HEAD 2>/dev/null || true)"
# Files actually touched by that commit — used to scope the trailer so a
# Release-bump trailer only counts when the commit really touched pkg/.
merge_touched_paths="$(git log -1 --name-only --format= HEAD 2>/dev/null || true)"

# bump_for_pkg <last-tag> — echo the next full tag. Pre-release tags refuse to
# bump (the shared lib rejects them in next_patch/minor).
bump_for_pkg() {
  local last="$1" trailer
  trailer="$(grep -E '^pkg/' <<<"$merge_touched_paths" >/dev/null 2>&1 && echo "$trailer_raw" || echo)"
  case "$trailer" in
    minor) next_minor "$last" ;;
    major)
      # v0.x → v1.0.0 is the normal stabilization step on the SAME bare module
      # (both majors are bare-module-path-legal). Only a breaking v2+ needs a
      # real …/pkg/v2 module path (deferred — ADR 0009).
      case "$last" in
        pkg/v0.*) echo "pkg/v1.0.0" ;;
        *)
          echo "cut-tags: 'Release-bump: major' rejected — a breaking v2 needs a real …/pkg/v2 module path (deferred — ADR 0009)" >&2
          return 1
          ;;
      esac
      ;;
    *) next_patch "$last" ;;
  esac
}

# internal_deps_of <go.mod> — echo the intra-repo internal module paths this
# go.mod requires, so we re-pin exactly those (never add a spurious require).
internal_deps_of() {
  go mod edit -json "$1" |
    jq -r '.Require[]?.Path | select(startswith("github.com/kitsunium/sdk/internal/"))'
}

# rewrite_publishable <go.mod> <sem> — drop every intra-repo `replace` and pin
# every intra-repo `require` to <sem> (e.g. v0.1.0). After this the module
# resolves from the proxy with no local `replace`.
rewrite_publishable() {
  local gomod="$1" sem="$2" dep
  local -a edits=()
  while IFS= read -r dep; do
    [ -z "$dep" ] && continue
    edits+=(-dropreplace="$dep" -require="$dep@$sem")
  done < <(internal_deps_of "$gomod")
  if [ "${#edits[@]}" -gt 0 ]; then
    go mod edit "${edits[@]}" "$gomod"
  fi
}

# assert_publishable <go.mod> — fail if any intra-repo `replace` survives or the
# file no longer parses. This is the gate we CAN run pre-push (the proxy-side
# `go mod download` proof needs the tags to exist — see header / ADR 0009).
assert_publishable() {
  local gomod="$1"
  go mod edit -json "$gomod" >/dev/null || {
    echo "cut-tags: $gomod does not parse after rewrite" >&2
    return 1
  }
  if go mod edit -json "$gomod" | jq -e \
    '.Replace // [] | map(select(.Old.Path | startswith("github.com/kitsunium/sdk/internal/"))) | length > 0' >/dev/null; then
    echo "cut-tags: $gomod still has an intra-repo replace after rewrite" >&2
    return 1
  fi
}

# publish_chain <sem> — on a detached worktree, rewrite every chain go.mod to
# the publishable form, then either (DRY_RUN) print the form + planned tags, or
# commit the rewrite on a detached release commit, tag the whole chain at <sem>,
# and atomic-push. The dev branch is never modified.
publish_chain() {
  local sem="$1"
  # Lockstep: every chain module is tagged at the same semver. internal/* + pkg
  # use bare module paths (major 0/1); a v2+ release would need /vN module paths
  # — deferred (ADR 0009). Fail loud rather than mint an invalid tag.
  case "$sem" in
    v0.* | v1.*) ;;
    *)
      echo "cut-tags: chain lockstep at $sem needs /vN module paths (deferred — ADR 0009)" >&2
      return 1
      ;;
  esac

  local -a chain_dirs=("${INTERNAL_MODULES[@]}" "pkg")
  local -a tags=()
  local d t
  for d in "${chain_dirs[@]}"; do tags+=("$d/$sem"); done

  # Validate every tag before touching the repo: pkg via the canonical regex,
  # internal/* via the internal regex.
  for t in "${tags[@]}"; do
    case "$t" in
      pkg/*) is_valid_tag "$t" ;;
      *) is_valid_internal_tag "$t" ;;
    esac || {
      echo "cut-tags: refusing to push malformed chain tag '$t'" >&2
      return 1
    }
  done

  local wt rc=0
  wt="$(mktemp -d)/rel"
  git worktree add --quiet --detach "$wt" HEAD
  (
    cd "$wt"
    for d in "${chain_dirs[@]}"; do
      rewrite_publishable "$d/go.mod" "$sem"
      assert_publishable "$d/go.mod"
    done
    if [ "$DRY_RUN" -eq 1 ]; then
      echo "DRY-RUN: publishable module graph for $sem (replace dropped, intra-repo deps pinned):"
      for d in "${chain_dirs[@]}"; do
        echo "  --- $d/go.mod ---"
        grep -nE 'replace|kitsunium/sdk/internal' "$d/go.mod" | sed 's/^/    /' || true
      done
      echo "DRY-RUN: would tag chain: ${tags[*]}"
    else
      git commit --quiet -am "release $sem — publishable module graph (no replace)"
      local relc
      relc="$(git rev-parse HEAD)"
      for t in "${tags[@]}"; do git tag -a "$t" -m "release $t" "$relc"; done
      git push --atomic origin "${tags[@]}"
    fi
  ) || rc=$?
  git worktree remove --force "$wt" 2>/dev/null || true
  return "$rc"
}

while IFS= read -r token; do
  [ -z "$token" ] && continue
  case "$token" in
    pkg) ;;
    # Backward-compat: a legacy "vN" major token (pre bare-`pkg` migration)
    # now maps to the single public module.
    v[0-9]*) token="pkg" ;;
    *)
      echo "cut-tags: unexpected bump token '$token' (expected 'pkg')" >&2
      exit 1
      ;;
  esac

  last="$(latest_pkg_tag || true)"
  if [ -z "$last" ]; then
    # First release: seed the alpha v0.1.0 directly. The bare `pkg` module path
    # carries major 0, so v0 is the correct (and only Go-legal) starting major.
    next="pkg/v0.1.0"
    if ! is_valid_tag "$next"; then
      echo "cut-tags: computed invalid first tag '$next'" >&2
      exit 1
    fi
    # Bootstrap guard (ADR 0009): the very first release publishes the chain's
    # go.mods to the proxy/sumdb PERMANENTLY. Its go.sum + clean-room `go get`
    # must be validated by hand first — so refuse to auto-cut it. A maintainer
    # re-runs with --allow-bootstrap once verified. Dry-run is always allowed.
    if [ "$DRY_RUN" -eq 0 ] && [ "$ALLOW_BOOTSTRAP" -eq 0 ]; then
      echo "cut-tags: refusing to auto-cut the FIRST release ($next). Validate the chain's go.sum + a clean-room 'go get' first (ADR 0009), then re-run with --allow-bootstrap." >&2
      exit 3
    fi
  else
    if ! is_valid_tag "$last"; then
      echo "cut-tags: refusing pre-release or malformed: $last" >&2
      exit 1
    fi
    next="$(bump_for_pkg "$last")"
  fi

  # Semver of the release (e.g. "v0.1.0"), shared by the whole chain.
  sem="v$(version_from_tag "$next")"

  # Race window close (plan B3): re-read latest tag immediately before the push
  # and abort if another job tagged in between.
  latest_now="$(latest_pkg_tag || true)"
  if [ -n "$latest_now" ] && [ "$latest_now" != "$last" ]; then
    echo "cut-tags: race detected — pkg moved from $last to $latest_now during prep; aborting" >&2
    exit 2
  fi

  publish_chain "$sem"

  # Echo only the pkg tag: it is the consumer-facing release (the internal/*
  # tags are resolution-only and get no GitHub Release).
  if [ "$DRY_RUN" -eq 0 ]; then
    echo "$next"
  fi
done
