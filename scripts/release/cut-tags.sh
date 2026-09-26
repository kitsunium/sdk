#!/usr/bin/env bash
# scripts/release/cut-tags.sh — read the bump token `pkg` on stdin (from
# compute-bumps.sh), compute the next tag, then publish a `go get`-able tag
# chain (ADR 0009): rewrite every chain module's go.mod to drop `replace` and pin
# its intra-repo deps to the release version, commit that on a detached release
# commit, and tag internal/{kernel,core,service} + pkg at the same version so a
# consumer resolves the whole graph from the proxy with no local context.
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
# The size comes from the `release:*` label a maintainer set on the merged pull
# requests of the range compute-bumps.sh diffs (ADR 0135) — or from --bump,
# which is how a maintainer states it for the whole range. It used to come from
# a `Release-bump:` trailer in the commit message, read over the same range
# (ADR 0085); the message is composed from contributor text, so the trailer
# could be buried by it or set by it, and lib/release-size.sh says why neither
# is survivable.
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
# shellcheck source=lib/release-scope.sh
. "$here/lib/release-scope.sh"
# shellcheck source=lib/release-size.sh
. "$here/lib/release-size.sh"

# Internal modules in pkg's publish chain (ADR 0001 fixes this set). They are
# tagged + cross-pinned at release so pkg resolves without `replace`; Go's
# internal/ rule still blocks direct consumer import — these tags exist only
# for module-graph resolution. Leaves first.
INTERNAL_MODULES=(internal/kernel internal/core internal/service)

DRY_RUN=0
ALLOW_BOOTSTRAP=0
RANGE=""
BUMP=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --allow-bootstrap) ALLOW_BOOTSTRAP=1 ;;
    --range=*) RANGE="${arg#--range=}" ;;
    --bump=*) BUMP="${arg#--bump=}" ;;
    --help | -h)
      cat <<EOF
cut-tags.sh — read the bump token 'pkg' on stdin, publish the next tag chain.

Usage: $0 [--dry-run] [--allow-bootstrap] [--range=<rev>..HEAD]
          [--bump=patch|minor|major] < bump.txt

The size is the largest 'release:patch|minor|major' label on the merged pull
requests behind the first-parent commits of the range that could cut a release
(ADR 0135); no label is a patch. A commit whose message asks for more than a
patch in a 'Release-bump:' line, with no label to decide it, is refused.
Without --range, infers the same range compute-bumps.sh does.

--bump states the size of the whole range and asks GitHub nothing. It is the
recovery path for a refused release: SDK Release's workflow_dispatch input
'bump' passes it.
EOF
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 64
      ;;
  esac
done

# A --bump that is not a size is a typo, and a typo on the one flag that
# overrides every label must not read as "patch". Refused before anything runs.
if [ -n "$BUMP" ] && ! size_rank "$BUMP" >/dev/null; then
  echo "cut-tags: --bump='$BUMP' is not a size (patch, minor or major)" >&2
  exit 64
fi

# The range whose merged pull requests size the release — the SAME range
# compute-bumps.sh diffs, via the same release_base() (ADR 0085).
#
# It used to be `git log -1 HEAD`, and that is the defect ADR 0085 fixed: a
# release only fires on a SUCCESSFUL CI run, and a run cancelled by the next
# push (the concurrency group) produces none at all. So the commit that asks for
# a size is routinely NOT the commit this script sees as HEAD — while
# compute-bumps.sh still saw its pkg/ changes inside the range and asked for a
# release. Reading HEAD alone then answered "patch" for a change the maintainer
# had signed as minor, silently. Labels do not change that: the label sits on
# the pull request of whichever commit it was, and the walk still has to reach
# that commit.
if [ -z "$RANGE" ]; then
  base="$(release_base)"
  # No baseline (bootstrap repo of one commit): every commit there is, i.e.
  # HEAD. Bootstrap never consults the size — the first tag is seeded at v0.1.0
  # below — but the range stays well-defined rather than malformed.
  RANGE="${base:+${base}..}HEAD"
fi

# A range git cannot walk must never read as "nothing asked for a size": that
# silent empty answer is the whole defect. release_base() hands back a verified
# commit, so only an explicit --range can produce an unwalkable one — checked
# once here rather than swallowed at every use.
if ! git rev-list --count "$RANGE" >/dev/null 2>&1; then
  echo "cut-tags: cannot walk the release range '$RANGE'" >&2
  exit 64
fi

# counts_for_release <sha> — 0 when <sha> touches at least one path that could
# CUT a release, which is exactly when the size its pull request carries is in
# scope (ADR 0089). One notion of "a file that counts", shared with
# compute-bumps.sh.
#
# Why `internal/` and not `pkg/` alone: ADR 0007 §2 requires a behavioural
# change in internal/** observable through pkg to be a MINOR, while its own next
# row gated the only way to ask for one on touching pkg/. There was no way to
# say what the table demanded. compute-bumps.sh already cuts a release for an
# internal/-only change whose rdeps reach //pkg/..., and that release tags the
# whole graph, so the change ships either way — this only lets its size be
# stated.
#
# Why maintainer-only metadata is excluded: compute-bumps.sh already drops it
# when deciding WHETHER to release ("its churn alone must not cut a release").
# Without the same exclusion here, a file that cannot trigger a release could
# still SIZE one — measured: a commit touching only pkg/v1/foo/CLAUDE.md with a
# minor request cut v0.2.0. A label is no different: a pull request that touches
# a BENCH.md and nothing else cannot size the release another merge cuts.
#
# WHICH files those are is decided once, in lib/release-scope.sh (#238), and
# this function only supplies the sha's path list.
#
# awk, not `grep -q`: grep exits on its first match, git takes SIGPIPE, and this
# repository has already lost a release to a 141 from that shape. awk consumes
# the whole stream and reports through its exit status, so git always finishes
# writing. No `grep -c` either — it prints 0 AND exits 1.
#
# `-m --first-parent`: a true merge shows NO files under a plain --name-only, so
# its size used to be scoped against an empty list and count for nothing.
counts_for_release() {
  release_scope_any < <(git log -1 --name-only --format= -m --first-parent "$1")
}

# range_size <range> — echo the size of the release: the largest size carried by
# a first-parent commit in <range> that could ALSO cut a release, `patch` when
# none carries more. Returns 1, with the reason on stderr, when a size cannot be
# decided without guessing.
#
# Why --first-parent, and why it is load-bearing rather than tidy: the size is a
# decision about the merges ON the branch. Walk the range without it and the
# walk descends into the commits a true merge brought IN — the contributor's own
# commits, whose messages ADR 0007 §2 never let size anything. The pull request
# behind a first-parent commit is the one a maintainer merged, which is where
# the label is.
#
# Why the scoping stays per commit: a label is a maintainer's decision about ONE
# merge, honoured only if that merge could cut a release (ADR 0089). Scoping
# the whole range at once would let any commit's paths vouch for any other
# commit's label.
#
# Why largest rather than newest: a size states what the release must be at
# least, so a later merge that says nothing cannot shrink one that did. Two
# merges each labelled minor coalesce into the single minor they both meant.
#
# Only the commits that could cut a release pay for the API call — the paths
# are read from git first, and a range of documentation merges asks GitHub
# nothing at all.
range_size() {
  local range="$1" shas="" sha="" short="" info="" prs="" labels="" rc=0
  local lsize="" msg="" ask="" size="" r=0 why="" err=""
  local best="patch" best_rank=0 best_sha="" best_from=""

  shas="$(git log --first-parent --format=%H "$range")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "cut-tags: git could not list the commits of '$range' (exit $rc) — refusing to size this release" >&2
    return 1
  fi

  # One file for every lookup's stderr, so a warning gh prints on success can
  # never be read back as a label.
  err="$(mktemp)"
  while IFS= read -r sha; do
    [ -z "$sha" ] && continue
    counts_for_release "$sha" || continue
    short="$(git rev-parse --short "$sha")"

    if ! info="$(pr_labels_of_commit "$sha" 2>"$err")"; then
      echo "cut-tags: $short could cut a release, and the pull request behind it could not be read — refusing to size this release" >&2
      sed 's/^/cut-tags:   /' "$err" >&2
      echo "cut-tags:   a lookup that failed is not a merge without a label (ADR 0135). Re-run once GitHub answers, or dispatch SDK Release with bump=<patch|minor|major>." >&2
      rm -f "$err"
      return 1
    fi
    prs="" labels=""
    if [ -n "$info" ]; then
      prs="${info%%$'\n'*}"
      case "$info" in *$'\n'*) labels="${info#*$'\n'}" ;; esac
    fi

    if ! lsize="$(label_size <<<"$labels" 2>"$err")"; then
      echo "cut-tags: $short ($prs): $(cat "$err") — refusing to size this release" >&2
      rm -f "$err"
      return 1
    fi

    msg="$(git log -1 --format=%B "$sha")"
    ask="$(text_ask <<<"$msg")"

    if ! size="$(decide_size "$lsize" "$ask")"; then
      if [ -n "$prs" ]; then
        why="$prs carries no release label"
      else
        why="no merged pull request introduced it"
      fi
      echo "cut-tags: $short asks for 'Release-bump: $ask' in its message, and $why — refusing to size this release" >&2
      echo "cut-tags:   a release is sized by a maintainer's label on the pull request, never by message text (ADR 0135)." >&2
      if [ -n "$prs" ]; then
        echo "cut-tags:   label $prs release:$ask (or release:patch to decline it), then re-run this job;" >&2
      fi
      echo "cut-tags:   or dispatch SDK Release with bump=<patch|minor|major>, which sizes the whole range." >&2
      rm -f "$err"
      return 1
    fi

    if [ -n "$lsize" ] && [ -n "$ask" ] && [ "$ask" != "$lsize" ]; then
      echo "cut-tags: $short ($prs): the message asks for '$ask', the label says '$lsize' — the label decides (ADR 0135)" >&2
    fi

    # Above patch only a label can reach, so best_from always names one.
    r="$(size_rank "$size")"
    if [ "$r" -gt "$best_rank" ]; then
      best_rank="$r"
      best="$size"
      best_sha="$sha"
      best_from="label release:$lsize on $prs ($short)"
    fi
  done <<<"$shas"
  rm -f "$err"

  # Say where the size came from. That is the only trace a maintainer reading
  # the run has of WHICH merge sized it, and when it is not HEAD it is the only
  # trace that a merge whose own CI run never produced a release did. stderr,
  # never stdout: stdout is the tag list the workflow feeds to `gh release`.
  if [ "$best_rank" -eq 0 ]; then
    echo "cut-tags: size patch — no merge in the range is labelled above patch — over $range" >&2
  elif [ "$best_sha" != "$(git rev-parse HEAD)" ]; then
    echo "cut-tags: size $best — $best_from, not HEAD — over $range" >&2
  else
    echo "cut-tags: size $best — $best_from — over $range" >&2
  fi
  echo "$best"
}

# The size, decided once. --bump is a maintainer's statement for the whole
# range and asks GitHub nothing; without it a refusal from range_size is fatal,
# because continuing would cut the patch the refusal exists to stop.
if [ -n "$BUMP" ]; then
  echo "cut-tags: size $BUMP — stated with --bump for the whole range $RANGE (ADR 0135)" >&2
  size="$BUMP"
else
  size="$(range_size "$RANGE")" || exit 65
fi

# bump_for_pkg <last-tag> — echo the next full tag. Pre-release tags refuse to
# bump (the shared lib rejects them in next_patch/minor).
bump_for_pkg() {
  local last="$1"
  case "$size" in
    minor) next_minor "$last" ;;
    major)
      # v0.x → v1.0.0 is the normal stabilization step on the SAME bare module
      # (both majors are bare-module-path-legal). Only a breaking v2+ needs a
      # real …/pkg/v2 module path (deferred — ADR 0009).
      case "$last" in
        pkg/v0.*) echo "pkg/v1.0.0" ;;
        *)
          echo "cut-tags: a major release from $last was refused — a breaking v2 needs a real …/pkg/v2 module path (deferred — ADR 0009)" >&2
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
      # `core.hooksPath=` (empty) rather than the project's hooks: this commit
      # is the ONE tree in the repository that the pre-commit gates cannot pass,
      # by construction. They run `make build` / `make test`, and the rewrite
      # above has just dropped every intra-repo `replace` — so Go and Bazel can
      # no longer resolve the sibling modules from disk, and the build fails on
      # a tree that is CORRECT for publication. Measured: the hook reports
      # "Couldn't start the build. Unable to run tests", make exits 48, and the
      # commit never happens.
      #
      # This never fired in CI, which checks out without `core.hooksPath` and so
      # runs no project hook at all — the defect only exists for a maintainer who
      # ran `scripts/install-hooks.sh`, which the root CLAUDE.md tells every
      # clone to do. What the gates would have checked is already checked: this
      # commit changes nothing but four go.mod files, and `assert_publishable`
      # verifies each of them above.
      local before
      before="$(git rev-parse HEAD)"
      git -c core.hooksPath= commit --quiet -am "release $sem — publishable module graph (no replace)"
      local relc
      relc="$(git rev-parse HEAD)"
      # A commit that did not happen must never be tagged. Without this the
      # chain is tagged at the UNREWRITTEN tree: `pkg/go.mod` keeps its local
      # `replace` lines and pins the previous release's internals, which is the
      # exact `go get`-resolvability ADR 0009 exists to guarantee — published,
      # and wrong. Observed on a real run before this guard existed.
      if [ "$relc" = "$before" ]; then
        echo "cut-tags: the release commit did not happen — refusing to tag $sem at the unrewritten tree" >&2
        return 1
      fi
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
