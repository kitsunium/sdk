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
# The bump size comes from a `Release-bump` trailer read over the same range
# compute-bumps.sh diffs, not from HEAD alone (ADR 0085).
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
RANGE=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --allow-bootstrap) ALLOW_BOOTSTRAP=1 ;;
    --range=*) RANGE="${arg#--range=}" ;;
    --help | -h)
      cat <<EOF
cut-tags.sh — read the bump token 'pkg' on stdin, publish the next patch tag chain.

Usage: $0 [--dry-run] [--allow-bootstrap] [--range=<rev>..HEAD] < bump.txt

Honors a 'Release-bump: minor' trailer on any merge commit since the last
release, but only on commits that actually touch pkg/; the largest such
trailer wins. Without --range, infers the same range compute-bumps.sh does.
EOF
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 64
      ;;
  esac
done

# The range over which the maintainer's Release-bump trailer is read — the SAME
# range compute-bumps.sh diffs, via the same release_base() (ADR 0085).
#
# It used to be `git log -1 HEAD`, and that is the defect: a release only fires
# on a SUCCESSFUL CI run, and a run cancelled by the next push (the concurrency
# group) produces none at all. So the commit carrying the trailer is routinely
# NOT the commit this script sees as HEAD — while compute-bumps.sh still saw its
# pkg/ changes inside the range and asked for a release. Reading HEAD alone then
# answered "patch" for a change the maintainer had signed as minor, silently.
if [ -z "$RANGE" ]; then
  base="$(release_base)"
  # No baseline (bootstrap repo of one commit): every commit there is, i.e.
  # HEAD. Bootstrap never consults the trailer — the first tag is seeded at
  # v0.1.0 below — but the range stays well-defined rather than malformed.
  RANGE="${base:+${base}..}HEAD"
fi

# A range git cannot walk must never read as "no trailer": that silent empty
# answer is the whole defect. release_base() hands back a verified commit, so
# only an explicit --range can produce an unwalkable one — checked once here
# rather than swallowed at every use.
if ! git rev-list --count "$RANGE" >/dev/null 2>&1; then
  echo "cut-tags: cannot walk the release range '$RANGE'" >&2
  exit 64
fi

# rank_of <value> — order the trailer vocabulary so "largest wins" is decidable.
# Anything unrecognised ranks with patch, which is what the bump already
# defaulted to: an unreadable trailer must never *raise* a release.
rank_of() {
  case "$1" in
    major) echo 2 ;;
    minor) echo 1 ;;
    *) echo 0 ;;
  esac
}

# range_trailer <range> — echo the largest Release-bump value carried by a
# commit in <range> that ALSO touched pkg/, or nothing.
#
# Why --first-parent, and why it is load-bearing rather than tidy: ADR 0007 §2
# gates a minor on a trailer "an attacker cannot smuggle through a PR body" by
# reading it from the merge commit — the one message a maintainer writes. Walk
# the range without --first-parent and it descends into the commits a merge
# brought IN, which are the contributor's own. That is not hypothetical here:
# five commits on this repository carry `Release-bump: minor`, touch pkg/, and
# are NOT on main's first-parent line. --first-parent keeps the authority where
# ADR 0007 put it, on main's own commits, and merely widens it from one of them
# to every one since the last release.
#
# Why the scoping stays per commit: the trailer is a maintainer's signature on
# ONE merge, honoured only for the paths that merge actually touched (ADR 0007
# §2). That is what stops a `Release-bump: minor` written in an unrelated docs
# merge from sizing a pkg release, and it only survives if each commit is
# matched against its OWN name-only list — scoping the whole range at once would
# let any commit's paths vouch for any other commit's trailer.
#
# Why `-m --first-parent` on that path check: a true merge commit shows NO files
# under a plain --name-only, so its trailer was scoped against an empty list and
# counted for nothing — 14 of this repository's 33 trailer-bearing commits are
# merges, and every one of them was silently worth a patch. `-m --first-parent`
# asks for the diff against parent 1, i.e. what the merge brought in, and is
# byte-identical on an ordinary single-parent commit.
#
# Why largest rather than newest: the trailer states what the release must be at
# least, so a later commit that says nothing cannot shrink one that did. Two
# commits each asking for minor coalesce into the single minor they both meant.
#
# Why the separator is %x1F and not the `,` this was written with: git parses
# the option list itself on commas, so `separator=,` consumes the comma as the
# list delimiter and leaves the separator EMPTY — measured on git 2.47.3, a
# commit carrying `Release-bump: mi` and `Release-bump: nor` yields the single
# field `minor` and cuts a minor release. A unit separator cannot be typed into
# a commit message by accident, and no trailer value can contain one.
#
# Only trailer-bearing commits pay for the second git call — the first pass
# reads every commit's trailer in one `git log`, and the path check then runs on
# the handful that carry one (usually zero).
range_trailer() {
  local best="" best_rank=0 best_sha="" sha="" raw="" value="" r=""
  local line="" msg_count=0 parsed_count=0
  while read -r sha raw; do
    # A trailer git did not parse is not a trailer git ignored — it is a bump
    # the maintainer asked for and nobody applied. `git interpret-trailers` and
    # `%(trailers:…)` read the LAST PARAGRAPH only, so a `Release-bump:` with
    # anything after it is invisible, and so is one whose paragraph contains a
    # malformed neighbour: `Refs #127` without the colon stops the block being a
    # block and takes the Release-bump down with it (measured, git 2.47.3).
    #
    # This is not hypothetical. Release v0.3.4, range 13a33b6b..e435af8c:
    # e435af8c (#207) carried `Release-bump: minor` at column 0 and touched three
    # files under pkg/, but the trailer sat at line 106 of a 138-line message
    # GitHub composed from the branch commits, with bullet sections after it. The
    # parser returned empty, next_patch won, and pkg/v0.3.4 shipped.
    # pkg/v0.4.0 has never existed. Nothing failed and nothing warned.
    #
    # Why refuse rather than read the trailer wherever it appears: the squash
    # message GitHub composes embeds the BRANCH COMMITS' own bodies, so reading
    # anywhere would let a contributor set the release size — the exact smuggling
    # ADR 0007 §2 exists to prevent. A refusal cannot set a size. It can only
    # stop a release, loudly, with the commit named.
    #
    # Counted rather than matched, so a message carrying two `Release-bump:`
    # lines of which git parsed one is caught too. Pure `case` on a variable:
    # no `grep -c` (which prints 0 and exits 1, a combination that has inverted
    # verdicts here before) and no pipeline into `grep -q`.
    msg_count=0
    while IFS= read -r line; do
      case "$line" in "Release-bump:"*) msg_count=$((msg_count + 1)) ;; esac
    done < <(git log -1 --format=%B "$sha")

    parsed_count=0
    if [ -n "$raw" ]; then
      while IFS= read -r value; do
        [ -n "$value" ] && parsed_count=$((parsed_count + 1))
      done < <(tr '\037' '\n' <<<"$raw")
    fi

    if [ "$msg_count" -gt "$parsed_count" ]; then
      # Only when the commit ALSO touched pkg/. A trailer on a commit that
      # touched nothing under pkg/ would not have counted even if it had parsed
      # (ADR 0007 §2), so refusing on it would be an alarm about nothing.
      if grep -qE '^pkg/' < <(git log -1 --name-only --format= -m --first-parent "$sha"); then
        echo "cut-tags: $(git rev-parse --short "$sha") carries a 'Release-bump:' OUTSIDE the trailer block — refusing to size this release" >&2
        echo "cut-tags:   the message has $msg_count, git parsed $parsed_count. Trailers are read from the LAST PARAGRAPH only." >&2
        echo "cut-tags:   move it to the last paragraph, alone or beside well-formed 'Key: value' trailers, and re-run." >&2
        return 1
      fi
    fi

    [ -z "$raw" ] && continue
    # Redirection, never `git log … | grep -qE`. `grep -q` exits on its first
    # match, git takes SIGPIPE, `pipefail` reports 141, and `|| continue` then
    # SKIPS the commit — so a merge touching pkg/ AND enough other paths to fill
    # the pipe buffer had its trailer discarded and the release fell back to a
    # patch. Which is ADR 0085's defect reopened by another route. Measured on
    # this shape: 400 paths (85 KB) fails 9 times in 10, 1600 paths 10 in 10, and
    # `pkg/` sorts early enough to be the FIRST match, which is the worst case.
    # A process substitution keeps git's status out of the pipeline entirely.
    grep -qE '^pkg/' < <(git log -1 --name-only --format= -m --first-parent "$sha") || continue
    # Split the field back into one value per repeated trailer, so a commit
    # carrying two of them is ranked like two commits would be. Default IFS on a
    # single-variable read, so `Release-bump: minor ` written with a stray space
    # still ranks as minor instead of silently as patch.
    while read -r value; do
      r="$(rank_of "$value")"
      if [ "$r" -gt "$best_rank" ]; then
        best_rank="$r"
        best="$value"
        best_sha="$sha"
      fi
    done < <(tr '\037' '\n' <<<"$raw")
  done < <(git log --first-parent --format="%H %(trailers:key=Release-bump,valueonly,separator=%x1F)" "$1")

  # Say where a winning trailer came from when it is not HEAD. That is the only
  # visible trace that a release was sized by a commit whose own CI run never
  # produced one, and it costs a log line. stderr, never stdout: stdout is the
  # tag list the workflow feeds to `gh release create`.
  if [ -n "$best" ] && [ "$best_sha" != "$(git rev-parse HEAD)" ]; then
    echo "cut-tags: 'Release-bump: $best' taken from $(git rev-parse --short "$best_sha"), not HEAD — over $1" >&2
  fi
  echo "$best"
}

# A refusal from range_trailer is fatal: continuing would cut the patch that the
# silent-drop bug used to cut, which is the outcome the refusal exists to stop.
trailer="$(range_trailer "$RANGE")" || exit 65

# bump_for_pkg <last-tag> — echo the next full tag. Pre-release tags refuse to
# bump (the shared lib rejects them in next_patch/minor).
bump_for_pkg() {
  local last="$1"
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
