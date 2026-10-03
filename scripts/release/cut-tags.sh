#!/usr/bin/env bash
# scripts/release/cut-tags.sh — read the release token on stdin (from
# compute-bumps.sh: `sdk`), compute the next SDK version, and publish ONE
# release (ADR 0162):
#
#   - the tag vX.Y.Z on the SDK module, github.com/kitsunium/sdk — the
#     repository root, which holds internal/, pkg/ and framework/;
#   - at the same X.Y.Z, the tag <dir>/vX.Y.Z of each module that requires a
#     vendor — third-party/* (ADR 0157), framework/connectors/* (ADR 0147,
#     ADR 0158) — whose files changed since its own last tag, or that has none.
#
# Each module tagged beside the SDK gets a go.mod that requires the SDK at
# vX.Y.Z and replaces nothing, on a detached release commit, so a consumer
# resolves it from the proxy with no local context (ADR 0009). A module that
# did not change keeps its last tag, whose go.mod requires the SDK release it
# was cut with; Go's minimum version selection takes the newer SDK when a
# consumer requires one. stdout is the list of tags pushed, the SDK's first:
# the workflow makes ONE GitHub release of it and lists the others in its notes.
#
# The dev branch is untouched — only the published tags carry the replace-free
# go.mods. go.work + `replace` keep local dev working as before.
#
# Race-protected (plan B3): re-reads the latest release tag immediately before
# the push and aborts on drift.
#
# The size comes from the `release:*` label a maintainer set on the merged pull
# requests of the range compute-bumps.sh diffs (ADR 0135) — or from --bump,
# which is how a maintainer states it for the whole range. It used to come from
# a `Release-bump:` trailer in the commit message, read over the same range
# (ADR 0085); the message is composed from contributor text, so the trailer
# could be buried by it or set by it, and lib/release-size.sh says why neither
# is survivable.
#
# The version continues the SDK's: the newest vX.Y.Z or, until the first one
# exists, the newest pkg/vX.Y.Z the chain before ADR 0162 was released as — so
# pkg/v0.17.0 and a minor make v0.18.0. That first root tag is the first
# release of the SDK module's content — the proxy knows the path only at
# v0.0.0, a 2024 tag of an earlier history —, so it is held like the very first
# release (ADR 0009): refused without --allow-bootstrap, which a maintainer's
# dispatch passes once the module zip and a clean-room `go get` are validated.

set -euo pipefail
shopt -s nullglob

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"
# shellcheck source=lib/release-scope.sh
. "$here/lib/release-scope.sh"
# shellcheck source=lib/release-size.sh
. "$here/lib/release-size.sh"

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
cut-tags.sh — read the release token ('sdk') on stdin, publish the next release:
one tag vX.Y.Z on the SDK module, and <dir>/vX.Y.Z for each vendor module that
changed since its own last tag (ADR 0162).

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

--allow-bootstrap authorises the first release of the SDK module — the very
first release, or the first root tag after the pkg/vX.Y.Z history. Without it
that release is refused with exit 3 (held), never cut by an automatic run.
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

# The tokens are read in full first, and before anything asks GitHub: a token
# this script does not know is a caller's mistake, refused for what it is.
# Every valid token means "the SDK changed", and a release is cut ONCE whatever
# their number — a second pass would find the tag it just pushed and cut the
# next patch on top of it. The tokens compute-bumps.sh emitted before ADR 0162
# (`pkg`, `framework`, `third-party`, and the older `vN`) each released the
# whole chain; they still mean the one release, so a dispatch typed from habit
# is not refused.
tokens=()
while IFS= read -r token; do
  [ -z "$token" ] && continue
  case "$token" in
    sdk) ;;
    pkg | framework | third-party | v[0-9]*) token="sdk" ;;
    *)
      echo "cut-tags: unexpected bump token '$token' (expected 'sdk')" >&2
      exit 1
      ;;
  esac
  tokens+=("$token")
done

if [ "${#tokens[@]}" -eq 0 ]; then
  exit 0
fi

# The range whose merged pull requests size the release — the SAME range
# compute-bumps.sh diffs, via the same release_base() (ADR 0085).
#
# It used to be `git log -1 HEAD`, and that is the defect ADR 0085 fixed: a
# release only fires on a SUCCESSFUL CI run, and a run cancelled by the next
# push (the concurrency group) produces none at all. So the commit that asks for
# a size is routinely NOT the commit this script sees as HEAD — while
# compute-bumps.sh still saw its changes inside the range and asked for a
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
# Why `internal/` and not the public packages alone: ADR 0007 §2 requires a
# behavioural change in internal/** observable through pkg to be a MINOR, while
# its own next row gated the only way to ask for one on touching pkg/. There was
# no way to say what the table demanded. compute-bumps.sh already cuts a release
# for an internal/-only change whose rdeps reach a published package, so the
# change ships either way — this only lets its size be stated.
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

# bump_for <last-tag> — echo the next SDK tag from an SDK or a pkg base.
# Pre-release tags refuse to bump (the shared lib rejects them in
# next_patch/minor).
bump_for() {
  local last="$1"
  case "$size" in
    minor) next_minor "$last" ;;
    major)
      # v0.x → v1.0.0 is the normal stabilization step on the SAME bare module
      # path (both majors are legal without a suffix). Only a breaking v2+ needs
      # a real …/v2 module path (deferred — ADR 0009).
      case "$(version_from_tag "$last")" in
        0.*) echo "v1.0.0" ;;
        *)
          echo "cut-tags: a major release from $last was refused — a breaking v2 needs a real …/v2 module path (deferred — ADR 0009)" >&2
          return 1
          ;;
      esac
      ;;
    *) next_patch "$last" ;;
  esac
}

# SDK_MODULE is the SDK, the repository root (ADR 0162). Every module of this
# repository is SDK_MODULE or a path beneath it.
SDK_MODULE="github.com/kitsunium/sdk"

# The module paths ADR 0162 retired: their packages are the SDK module's now,
# so a go.mod that still requires one would put two modules providing the same
# packages in a consumer's build — an ambiguous import.
is_retired_module() {
  case "$1" in
    "$SDK_MODULE/pkg" | "$SDK_MODULE/framework" | "$SDK_MODULE/internal/"*) return 0 ;;
  esac
  return 1
}

# sdk_requires_of <go.mod> — the SDK-repository module paths this go.mod
# requires, the SDK itself included, so exactly those are re-pinned and no
# spurious require is added.
sdk_requires_of() {
  go mod edit -json "$1" |
    jq -r --arg m "$SDK_MODULE" '.Require[]?.Path | select(. == $m or startswith($m + "/"))'
}

# sdk_replaces_of <go.mod> — the SDK-repository module paths this go.mod
# replaces. Every one goes: the published go.mod must not point at a directory
# no consumer has.
sdk_replaces_of() {
  go mod edit -json "$1" |
    jq -r --arg m "$SDK_MODULE" '.Replace // [] | .[].Old.Path | select(. == $m or startswith($m + "/"))'
}

# in_list <word> <list…> — 0 when <word> is one of <list>.
in_list() {
  local w="$1" x
  shift
  for x in "$@"; do
    if [ "$x" = "$w" ]; then return 0; fi
  done
  return 1
}

# rewrite_publishable <go.mod> <sem> <tagged-dir…> — drop every SDK-repository
# `replace` and pin every SDK-repository `require`: the SDK at <sem>; a vendor
# module this release tags at <sem> too; any other vendor module at its own
# last tag. A requirement of a module ADR 0162 retired, or of a module that has
# no tag and gets none, is refused: there is no version to pin it to.
rewrite_publishable() {
  local gomod="$1" sem="$2" dep dir lt
  shift 2
  local -a edits=()
  while IFS= read -r dep; do
    [ -z "$dep" ] && continue
    if [ "$dep" = "$SDK_MODULE" ]; then
      edits+=(-require="$dep@$sem")
    elif is_retired_module "$dep"; then
      echo "cut-tags: $gomod requires $dep, a module ADR 0162 retired — require $SDK_MODULE instead" >&2
      return 1
    else
      dir="${dep#"$SDK_MODULE"/}"
      if in_list "$dir" "$@"; then
        edits+=(-require="$dep@$sem")
      elif lt="$(latest_vendor_tag "$dir")" && [ -n "$lt" ]; then
        edits+=(-require="$dep@v$(version_from_tag "$lt")")
      else
        echo "cut-tags: $gomod requires $dep, which has no tag and is not tagged by this release" >&2
        return 1
      fi
    fi
  done < <(sdk_requires_of "$gomod")
  while IFS= read -r dep; do
    [ -z "$dep" ] && continue
    edits+=(-dropreplace="$dep")
  done < <(sdk_replaces_of "$gomod")
  if [ "${#edits[@]}" -gt 0 ]; then
    go mod edit "${edits[@]}" "$gomod"
  fi
}

# assert_publishable <go.mod> — fail if the file no longer parses, if an
# SDK-repository `replace` survives, if it requires a module ADR 0162 retired,
# or if an SDK-repository requirement is left at a pseudo-version — the
# v0.0.0-00010101000000-000000000000 of a local replace, which resolves to
# nothing once the replace is gone. This is the gate we CAN run pre-push (the
# proxy-side `go mod download` proof needs the tags to exist — see header / ADR
# 0009). The SDK's own go.mod goes through it too: it requires no
# SDK-repository module at all.
assert_publishable() {
  local gomod="$1" json="" path="" version=""
  json="$(go mod edit -json "$gomod")" || {
    echo "cut-tags: $gomod does not parse after rewrite" >&2
    return 1
  }
  if jq -e --arg m "$SDK_MODULE" \
    '.Replace // [] | map(select(.Old.Path == $m or (.Old.Path | startswith($m + "/")))) | length > 0' \
    >/dev/null <<<"$json"; then
    echo "cut-tags: $gomod still has an SDK-repository replace after rewrite" >&2
    return 1
  fi
  while read -r path version; do
    [ -z "$path" ] && continue
    if is_retired_module "$path"; then
      echo "cut-tags: $gomod requires $path, a module ADR 0162 retired" >&2
      return 1
    fi
    # Every pseudo-version form ends in a 14-digit timestamp and a 12-hex
    # revision, whatever precedes them.
    if { [ "$path" = "$SDK_MODULE" ] || [ "${path#"$SDK_MODULE"/}" != "$path" ]; } &&
      [[ "$version" =~ [0-9]{14}-[0-9a-f]{12}$ ]]; then
      echo "cut-tags: $gomod requires $path at the pseudo-version $version, which names no release" >&2
      return 1
    fi
  done < <(jq -r '.Require[]? | "\(.Path) \(.Version)"' <<<"$json")
}

# vendor_changed <dir> <last-tag> — 0 when a file of the module in <dir> that
# could carry a consumer-visible change (lib/release-scope.sh) differs between
# the commit <last-tag> was cut from and HEAD; 1 when none does; 2, with the
# reason on stderr, when that cannot be told — never read as "unchanged".
#
# From the tag's FIRST PARENT, not the tag: the release commit rewrote the
# module's go.mod, so measured from the tag every module would always look
# changed. A module nested inside <dir> is another module, and its files are
# left out of <dir>'s question.
vendor_changed() {
  local dir="$1" tag="$2" base="" paths="" rc=0 other
  local -a spec=("$dir/")
  base="$(tag_base "$tag")" || {
    echo "cut-tags: $tag names no commit with a parent — cannot tell whether $dir changed since it" >&2
    return 2
  }
  for other in ${vendors[@]+"${vendors[@]}"}; do
    case "$other" in "$dir"/*) spec+=(":(exclude)$other/") ;; esac
  done
  paths="$(git diff --name-only "$base" HEAD -- "${spec[@]}")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "cut-tags: git diff for $dir since $tag failed (exit $rc) — refusing to guess whether it changed" >&2
    return 2
  fi
  release_scope_any <<<"$paths"
}

# publish_release <sem> <tagged-dir…> — on a detached worktree, rewrite the
# go.mod of every vendor module tagged at <sem> to the publishable form, check
# the SDK's own, then either (DRY_RUN) print the form + planned tags, or commit
# on a detached release commit, tag the SDK and those modules at <sem>, and
# atomic-push. The dev branch is never modified.
publish_release() {
  local sem="$1"
  shift
  # Bare module paths carry major 0/1 only; a v2+ release would need /vN module
  # paths — deferred (ADR 0009). Fail loud rather than mint an invalid tag.
  case "$sem" in
    v0.* | v1.*) ;;
    *)
      echo "cut-tags: a release at $sem needs /vN module paths (deferred — ADR 0009)" >&2
      return 1
      ;;
  esac

  local d t
  local -a tags=("$sem")
  for d in "$@"; do tags+=("$d/$sem"); done

  # Validate every tag before touching the repo, each against the shape of its
  # family: the SDK's, third-party/*, framework/connectors/* — lib/tag-format.sh.
  for t in "${tags[@]}"; do
    is_valid_release_tag "$t" || {
      echo "cut-tags: refusing to push malformed release tag '$t'" >&2
      return 1
    }
  done

  local wt rc=0
  wt="$(mktemp -d)/rel"
  git worktree add --quiet --detach "$wt" HEAD
  # The subshell runs under its OWN `set -e`, and its status is read after it
  # rather than by `( … ) || rc=$?`. Bash ignores errexit inside a subshell
  # whose status a `||` tests, so in that shape a failed rewrite or a refused
  # go.mod (assert_publishable returning 1) went on to the next line, and the
  # release was committed, tagged and pushed anyway. Measured: a vendor go.mod
  # still requiring github.com/kitsunium/sdk/pkg printed its refusal and
  # `would tag` both, exit 0. The `set +e` is the caller's errexit, held off
  # just long enough to clean the worktree up before the status is returned.
  set +e
  (
    set -e
    cd "$wt"
    assert_publishable go.mod
    for d in "$@"; do
      rewrite_publishable "$d/go.mod" "$sem" "$@"
      assert_publishable "$d/go.mod"
    done
    if [ "$DRY_RUN" -eq 1 ]; then
      echo "DRY-RUN: publishable go.mod files for $sem (replace dropped, the SDK pinned):"
      if [ "$#" -eq 0 ]; then
        echo "  (no vendor module changed: the SDK's go.mod is published as it is)"
      fi
      for d in "$@"; do
        echo "  --- $d/go.mod ---"
        grep -nE 'replace|kitsunium/sdk' "$d/go.mod" | sed 's/^/    /' || true
      done
      echo "DRY-RUN: would tag: ${tags[*]}"
    else
      # `core.hooksPath=` (empty) rather than any hooks the clone sets: this
      # commit is the ONE tree in the repository that the pre-commit gates
      # cannot pass, by construction. They run `make build` / `make test`, and
      # the rewrite above has just dropped the vendor modules' `replace` of the
      # SDK — so Go and Bazel can no longer resolve it from disk, and the build
      # fails on a tree that is CORRECT for publication. Measured: the hook
      # reports "Couldn't start the build. Unable to run tests", make exits 48,
      # and the commit never happens.
      #
      # This never fired in CI, which checks out without `core.hooksPath` and so
      # runs no project hook at all — the defect existed for a maintainer whose
      # clone pointed `core.hooksPath` at the in-repo hooks, which every clone
      # was told to do until ADR 0153 removed them; a clone may still carry a
      # hooks path of its own. What the gates would have checked is already
      # checked: this commit changes nothing but go.mod files, and
      # `assert_publishable` verifies each of them above.
      #
      # `--allow-empty`: a release in which no vendor module changed rewrites
      # nothing — the SDK's go.mod requires nothing of this repository — and its
      # tag must still sit on a release commit whose FIRST PARENT is the main
      # commit it was cut from, because that is where release_base() and every
      # vendor module's next measurement start. A tag on the main commit itself
      # would make the next range reopen it.
      local before
      before="$(git rev-parse HEAD)"
      git -c core.hooksPath= commit --quiet --allow-empty -am "release $sem — publishable module graph (no replace)"
      local relc
      relc="$(git rev-parse HEAD)"
      # A commit that did not happen must never be tagged. Without this the
      # release is tagged at the UNREWRITTEN tree: a vendor go.mod keeps its
      # local `replace` and pins nothing, which is the exact `go get`
      # unresolvability ADR 0009 exists to prevent — published, and wrong.
      # Observed on a real run before this guard existed.
      if [ "$relc" = "$before" ]; then
        echo "cut-tags: the release commit did not happen — refusing to tag $sem at the unrewritten tree" >&2
        return 1
      fi
      for t in "${tags[@]}"; do git tag -a "$t" -m "release $t" "$relc"; done
      git push --atomic origin "${tags[@]}"
    fi
  )
  rc=$?
  set -e
  git worktree remove --force "$wt" 2>/dev/null || true
  return "$rc"
}

# The version: the next one after the last release — the newest SDK tag, or,
# until the first one exists, the newest pkg tag (ADR 0162). latest_release_tag
# takes the higher of the two.
last_sdk="$(latest_sdk_tag || true)"
last="$(latest_release_tag || true)"
first=0
if [ -z "$last" ]; then
  # First release ever: seed the alpha v0.1.0 directly. The bare module path
  # carries major 0, so v0 is the correct (and only Go-legal) starting major.
  next="v0.1.0"
  first=1
else
  if ! is_valid_base "$last"; then
    echo "cut-tags: refusing pre-release or malformed: $last" >&2
    exit 1
  fi
  next="$(bump_for "$last")"
  if [ -z "$last_sdk" ]; then
    first=1
  fi
fi
if ! is_valid_tag "$next"; then
  echo "cut-tags: computed invalid tag '$next'" >&2
  exit 1
fi

# Bootstrap guard (ADR 0009): the first release of the SDK module publishes its
# go.mod and zip to the proxy and the checksum database PERMANENTLY — the very
# first release, and the first root tag after the pkg history, the first
# release of the module's content (ADR 0162). Its zip and a clean-room `go get`
# must be validated by hand first — so refuse to auto-cut it. A maintainer re-runs with --allow-bootstrap once
# verified. Dry-run is always allowed.
if [ "$first" -eq 1 ] && [ "$DRY_RUN" -eq 0 ] && [ "$ALLOW_BOOTSTRAP" -eq 0 ]; then
  if [ -z "$last" ]; then
    echo "cut-tags: refusing to auto-cut the FIRST release ($next). Validate its go.sum + a clean-room 'go get' first (ADR 0009), then re-run with --allow-bootstrap." >&2
  else
    echo "cut-tags: refusing to auto-cut the FIRST release of the SDK module ($next, after $last — ADR 0162). Validate its module zip + a clean-room 'go get' first (ADR 0009), then re-run with --allow-bootstrap." >&2
  fi
  exit 3
fi
if [ "$first" -eq 1 ] && [ -n "$last" ]; then
  echo "cut-tags: $next is the SDK module's first tag; it continues $last (ADR 0162)" >&2
fi

# Semver of the release (e.g. "v0.18.0"): the SDK's tag, and every vendor tag's
# version.
sem="$next"

# The vendor modules: every one tagged at <sem> when it changed since its own
# last tag or has none, and named on stderr either way — the only trace a run
# leaves of why a module was, or was not, released.
vendors=()
vendor_list="$(vendor_modules go.work)" || exit 1
if [ -n "$vendor_list" ]; then
  mapfile -t vendors <<<"$vendor_list"
fi
tagged=()
for d in ${vendors[@]+"${vendors[@]}"}; do
  lt="$(latest_vendor_tag "$d")"
  if [ -z "$lt" ]; then
    echo "cut-tags: $d has never been tagged — tagged at $sem" >&2
    tagged+=("$d")
    continue
  fi
  vrc=0
  vendor_changed "$d" "$lt" || vrc=$?
  case "$vrc" in
    0)
      echo "cut-tags: $d changed since $lt — tagged at $sem" >&2
      tagged+=("$d")
      ;;
    1) echo "cut-tags: $d unchanged since $lt — keeps it" >&2 ;;
    *) exit 1 ;;
  esac
done

# Race window close (plan B3): re-read the last release tag immediately before
# the push and abort if another job tagged in between.
latest_now="$(latest_release_tag || true)"
if [ -n "$latest_now" ] && [ "$latest_now" != "$last" ]; then
  echo "cut-tags: race detected — the last release moved from ${last:-(none)} to $latest_now during prep; aborting" >&2
  exit 2
fi

publish_release "$sem" ${tagged[@]+"${tagged[@]}"}

# stdout is the list of tags pushed: the SDK's first — the one the workflow
# makes a GitHub release of and its verification step counts — then the vendor
# modules', which that release's notes list.
if [ "$DRY_RUN" -eq 0 ]; then
  echo "$sem"
  for d in ${tagged[@]+"${tagged[@]}"}; do
    echo "$d/$sem"
  done
fi
