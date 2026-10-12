#!/usr/bin/env bash
# scripts/release/cut-tags.sh — read on stdin the modules to release (from
# compute-bumps.sh: one directory per line, `.` the SDK module), compute the
# next version of EACH, and publish them as one release (ADR 0170, amending
# ADR 0162):
#
#   - each module go.work names is tagged at its OWN version: `vX.Y.Z` for the
#     SDK module at the root, `<dir>/vX.Y.Z` for a pkg family
#     (`pkg/v1/<family>`), the shared `internal` module, and each module that
#     requires a vendor — third-party/* (ADR 0157), framework/connectors/*
#     (ADR 0147, ADR 0158);
#   - the version continues the module's own last tag, sized by the labels of
#     the merges that changed THAT module; a module never tagged starts at
#     v0.1.0, which only a path no release has published may do;
#   - the legacy token `sdk` (and `pkg`, `framework`, `third-party`, `vN`)
#     still means ADR 0162's release: the SDK module plus every other module
#     that changed since its own last tag, measured here.
#
# Each module tagged gets a go.mod that requires every module of this
# repository at the version this release tags it at, or at its own last tag,
# and replaces nothing, on ONE detached release commit, so a consumer resolves
# it from the proxy with no local context (ADR 0009). A module that did not
# change keeps its last tag; Go's minimum version selection takes a newer one
# when a consumer requires it. stdout is the list of tags pushed, in go.work's
# order (the SDK's first when it is cut): the workflow makes ONE GitHub release
# of the first and lists the others in its notes; --manifest writes the
# release's identity as release-manifest.json.
#
# The dev branch is untouched — only the published tags carry the replace-free
# go.mods. go.work + `replace` keep local dev working as before.
#
# Race-protected: `git fetch --prune --prune-tags origin` before anything is
# read, every module's last tag re-read before the publication, and EACH exact
# ref checked on origin right before the tags are created — any drift aborts
# with exit 2 and nothing published.
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
# release (ADR 0009) whenever its base is a pkg/vX.Y.Z tag, whatever root tag
# sits below it: refused without --allow-bootstrap, which a maintainer's
# dispatch passes once the module zip and a clean-room `go get` are validated.
# From that base a patch is refused outright (exit 65): the module change every
# consumer makes is a minor at least.
#
# That release, and no other, also cuts two tombstones (ADR 0162): pkg/vX.Y.Z
# and framework/vX.Y.Z at its version, each on a child of the release commit
# whose module directory holds a go.mod alone — a `// Deprecated:` notice that
# names the SDK module and the command that migrates to it, the module's own
# path, the SDK's go directive, and a requirement of the SDK at vX.Y.Z. Go
# resolves an import that nothing in a go.mod provides to the longest module
# path whose LATEST version provides the package; with the tombstones that is
# the SDK module, without them …/pkg and …/framework at v0.17.0, for good. Go
# reads a module's deprecation from its latest go.mod as well, so `go get` and
# `go list -m -u` tell a consumer still on either module where it went. They
# go out in the same atomic push as the root tag, and a tombstone tag that
# already exists is refused before anything is tagged.

set -euo pipefail
shopt -s nullglob

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"
# shellcheck source=lib/release-scope.sh
. "$here/lib/release-scope.sh"
# shellcheck source=lib/release-size.sh
. "$here/lib/release-size.sh"
# shellcheck source=lib/release-modules.sh
. "$here/lib/release-modules.sh"

DRY_RUN=0
ALLOW_BOOTSTRAP=0
RANGE=""
BUMP=""
MANIFEST=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --allow-bootstrap) ALLOW_BOOTSTRAP=1 ;;
    --range=*) RANGE="${arg#--range=}" ;;
    --bump=*) BUMP="${arg#--bump=}" ;;
    --manifest=*) MANIFEST="${arg#--manifest=}" ;;
    --help | -h)
      cat <<EOF
cut-tags.sh — read on stdin the modules to release (compute-bumps.sh's output:
one directory per line, '.' the SDK module; or 'sdk', the SDK module and every
other module that changed since its own last tag), and publish the next tag of
each, at its OWN version (ADR 0170).

Usage: $0 [--dry-run] [--allow-bootstrap] [--range=<rev>..HEAD]
          [--bump=patch|minor|major] [--manifest=<file>] < modules.txt

Each module's size is the largest 'release:patch|minor|major' label on the
merged pull requests behind the first-parent commits of ITS range — from the
first parent of its own last tag — that changed a path of it which could cut a
release (ADR 0135, ADR 0170); no label is a patch. A commit whose message asks
for more than a patch in a 'Release-bump:' line, with no label to decide it, is
refused. A module never tagged starts at v0.1.0. --range sizes every module
over the one range given.

--manifest writes release-manifest.json there: the release's identity (its
first tag), every module tagged with its version, previous tag and size, the
tombstones, and the commits (ADR 0170). Written by a dry run too.

--bump states the size of the whole range and asks GitHub nothing. It is the
recovery path for a refused release: SDK Release's workflow_dispatch input
'bump' passes it.

--allow-bootstrap authorises the first release of the SDK module — the very
first release, or the first root tag after the pkg/vX.Y.Z history. Without it
that release is refused with exit 3 (held), never cut by an automatic run.
The first root tag after the pkg history also cuts pkg/vX.Y.Z and
framework/vX.Y.Z at its version: tombstones, a go.mod that requires it and
deprecates its module with the migration command, and no package, in the same
push (ADR 0162).
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

# in_list <word> <list…> — 0 when <word> is one of <list>.
in_list() {
  local w="$1" x
  shift
  for x in "$@"; do
    if [ "$x" = "$w" ]; then return 0; fi
  done
  return 1
}

# The modules a release may tag, from go.work (lib/release-modules.sh), refused
# rather than guessed. Read before the tokens, which name them.
module_list=""
module_list="$(release_modules go.work)" || exit 1
modules=()
mapfile -t modules <<<"$module_list"

# The tokens are read in full first, and before anything asks GitHub: a token
# this script does not know is a caller's mistake, refused for what it is.
#
#   <dir>   the module in <dir> — `.` the SDK module at the root, or any other
#           module go.work names — and that module only (compute-bumps.sh's
#           output since ADR 0170: each module it found changed);
#   sdk     the SDK module AND every other module that changed since its own
#           last tag or was never tagged — what one release was under ADR 0162,
#           measured here. `pkg`, `framework`, `third-party` and the older `vN`
#           mean it too, so a dispatch typed from habit is not refused.
#
# Every module is cut ONCE whatever the number of tokens naming it: a second
# pass would find the tag it just pushed and cut the next patch on top of it.
AUTO=0
explicit=()
while IFS= read -r token; do
  [ -z "$token" ] && continue
  case "$token" in
    sdk | pkg | framework | third-party | v[0-9]*) AUTO=1 ;;
    *)
      if ! in_list "$token" "${modules[@]}"; then
        echo "cut-tags: unexpected bump token '$token' (expected 'sdk', '.' or a module directory go.work names: ${modules[*]})" >&2
        exit 1
      fi
      explicit+=("$token")
      ;;
  esac
done

if [ "$AUTO" -eq 0 ] && [ "${#explicit[@]}" -eq 0 ]; then
  exit 0
fi

# A --range, when given, is the range EVERY module is measured and sized over
# (the recovery path for a range a maintainer names). One git cannot walk must
# never read as "nothing asked for a size": that silent empty answer is the
# whole defect ADR 0085 closed. Without it, each module's range starts at the
# first parent of its own last tag (lib/release-modules.sh).
if [ -n "$RANGE" ] && ! git rev-list --count "$RANGE" >/dev/null 2>&1; then
  echo "cut-tags: cannot walk the release range '$RANGE'" >&2
  exit 64
fi

# What GitHub said about each first-parent commit, asked once per commit
# however many modules it touched: a merge that changed two families sizes
# both from ONE lookup, and the suite's call count says so.
declare -A PR_INFO=()

# range_size <range> <dir> — set RANGE_SIZE to the size of the release of the
# module in <dir>: the largest size carried by a first-parent commit in <range>
# that changed a path of THAT module which could cut a release, `patch` when
# none carries more. Returns 1, with the reason on stderr, when a size cannot be
# decided without guessing. A global rather than stdout, so the lookups it
# caches survive from one module to the next (a command substitution is a
# subshell).
#
# Why per module (ADR 0170): a label is a maintainer's decision about ONE merge,
# and that merge changed some modules and not others. Read for the whole range
# at once, a `release:minor` on a pull request touching pkg/v1/data would have
# moved every module the release cut — the lockstep a module per family exists
# to end. Scoped per module, it sizes exactly the modules its merge touched.
#
# Why --first-parent, and why it is load-bearing rather than tidy: the size is a
# decision about the merges ON the branch. Walk the range without it and the
# walk descends into the commits a true merge brought IN — the contributor's own
# commits, whose messages ADR 0007 §2 never let size anything. The pull request
# behind a first-parent commit is the one a maintainer merged, which is where
# the label is.
#
# Why the scoping stays per commit: a label is honoured only if its merge could
# cut a release (ADR 0089). Scoping the whole range at once would let any
# commit's paths vouch for any other commit's label.
#
# Why largest rather than newest: a size states what the release must be at
# least, so a later merge that says nothing cannot shrink one that did.
#
# Only the commits that touched the module pay for the API call — the paths are
# read from git first, and a range of documentation merges asks GitHub nothing.
range_size() {
  local range="$1" dir="$2" shas="" sha="" short="" info="" prs="" labels="" rc=0
  local lsize="" msg="" ask="" size="" r=0 why="" err="" trc=0 who=""
  local best="patch" best_rank=0 best_sha="" best_from=""
  RANGE_SIZE=""
  if [ "$dir" = "." ]; then who="the SDK module"; else who="$dir"; fi

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
    # The commit's paths, and a git that failed is a refusal: it used to be a
    # process substitution into the classifier, whose status hid git's, so a
    # commit git could not read counted as one that touched nothing.
    trc=0
    commit_touches_module "$sha" "$dir" "$module_list" 2>"$err" || trc=$?
    case "$trc" in
      0) ;;
      1) continue ;;
      *)
        echo "cut-tags: the paths of $sha could not be read — refusing to size this release" >&2
        sed 's/^/cut-tags:   /' "$err" >&2
        rm -f "$err"
        return 1
        ;;
    esac
    short="$(git rev-parse --short "$sha")"

    if [ -z "${PR_INFO[$sha]+set}" ]; then
      if ! info="$(pr_labels_of_commit "$sha" 2>"$err")"; then
        echo "cut-tags: $short could cut a release, and the pull request behind it could not be read — refusing to size this release" >&2
        sed 's/^/cut-tags:   /' "$err" >&2
        echo "cut-tags:   a lookup that failed is not a merge without a label (ADR 0135). Re-run once GitHub answers, or dispatch SDK Release with bump=<patch|minor|major>." >&2
        rm -f "$err"
        return 1
      fi
      PR_INFO[$sha]="$info"
    fi
    info="${PR_INFO[$sha]}"
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

  # Say where the size came from — the only trace a maintainer reading the run
  # has of WHICH merge sized it. stderr, never stdout: stdout is the tag list
  # the workflow feeds to `gh release`.
  if [ "$best_rank" -eq 0 ]; then
    echo "cut-tags: size patch for $who — no merge in the range is labelled above patch — over $range" >&2
  elif [ "$best_sha" != "$(git rev-parse HEAD)" ]; then
    echo "cut-tags: size $best for $who — $best_from, not HEAD — over $range" >&2
  else
    echo "cut-tags: size $best for $who — $best_from — over $range" >&2
  fi
  RANGE_SIZE="$best"
}

# bump_for <last-tag> <size> — the next tag of the module <last-tag> belongs to.
# The root's arithmetic is lib/tag-format.sh's, which also continues pkg's
# history; any other module's keeps its own prefix. A pre-release base refuses.
bump_for() {
  local last="$1" size="$2" ver="" prefix="" major minor patch
  case "$size" in
    major)
      # v0.x → v1.0.0 is the normal stabilization step on the SAME bare module
      # path (both majors are legal without a suffix). Only a breaking v2+ needs
      # a real …/v2 module path (deferred — ADR 0009).
      case "$(version_from_tag "$last")" in
        0.*)
          if is_valid_base "$last"; then echo "v1.0.0"; else echo "${last%/v*}/v1.0.0"; fi
          return 0
          ;;
        *)
          echo "cut-tags: a major release from $last was refused — a breaking v2 needs a real …/v2 module path (deferred — ADR 0009)" >&2
          return 1
          ;;
      esac
      ;;
  esac
  if is_valid_base "$last"; then
    case "$size" in
      minor) next_minor "$last" ;;
      *) next_patch "$last" ;;
    esac
    return
  fi
  is_valid_release_tag "$last" || {
    echo "cut-tags: invalid tag '$last'" >&2
    return 1
  }
  ver="$(version_from_tag "$last")"
  case "$ver" in
    *-*)
      echo "cut-tags: refusing to bump from pre-release '$last'" >&2
      return 1
      ;;
  esac
  prefix="${last%/v*}/"
  IFS=. read -r major minor patch <<<"$ver"
  case "$size" in
    minor) printf '%sv%s.%s.0\n' "$prefix" "$major" "$((minor + 1))" ;;
    *) printf '%sv%s.%s.%s\n' "$prefix" "$major" "$minor" "$((patch + 1))" ;;
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

# rewrite_publishable <go.mod> — drop every SDK-repository `replace` and pin
# every SDK-repository `require`: a module this release tags at the version it
# is tagged at (NEXT_VERSION); any other module at its own last tag. A
# requirement of a module ADR 0162 retired, or of a module that has no tag and
# gets none, is refused: there is no version to pin it to. Each module has its
# own version since ADR 0170, so the version is looked up per requirement —
# never "the release's", which no longer exists.
rewrite_publishable() {
  local gomod="$1" dep dir lt
  local -a edits=()
  while IFS= read -r dep; do
    [ -z "$dep" ] && continue
    if is_retired_module "$dep"; then
      echo "cut-tags: $gomod requires $dep, a module ADR 0162 retired — require $SDK_MODULE instead" >&2
      return 1
    fi
    if [ "$dep" = "$SDK_MODULE" ]; then dir="."; else dir="${dep#"$SDK_MODULE"/}"; fi
    if [ -n "${NEXT_VERSION[$dir]+set}" ]; then
      edits+=(-require="$dep@${NEXT_VERSION[$dir]}")
    elif lt="$(latest_module_tag "$dir")" && [ -n "$lt" ] && ! is_valid_pkg_tag "$lt"; then
      edits+=(-require="$dep@v$(version_from_tag "$lt")")
    else
      echo "cut-tags: $gomod requires $dep, which has no tag and is not tagged by this release" >&2
      return 1
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

# module_changed <dir> <last-tag> — 0 when a file of the module in <dir> that
# could carry a consumer-visible change (lib/release-scope.sh) differs between
# the commit <last-tag> was cut from and HEAD; 1 when none does; 2, with the
# reason on stderr, when that cannot be told — never read as "unchanged".
#
# From the tag's FIRST PARENT, not the tag: the release commit rewrote the
# module's go.mod, so measured from the tag every module would always look
# changed. A module nested inside <dir> is another module, and its files are
# left out of <dir>'s question (lib/release-modules.sh). A test file or a
# fixture is no change a consumer builds (#295).
module_changed() {
  local dir="$1" tag="$2" base="" paths="" rc=0
  base="$(tag_base "$tag")" || {
    echo "cut-tags: $tag names no commit with a parent — cannot tell whether $dir changed since it" >&2
    return 2
  }
  paths="$(module_paths "$dir" "$base..HEAD" "$module_list")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "cut-tags: git diff for $dir since $tag failed — refusing to guess whether it changed" >&2
    return 2
  fi
  release_scope_any <<<"$paths"
}

# TOMBSTONES are the module directories whose tombstone the first root tag cuts
# (ADR 0162): the two retired modules a consumer imported. Not internal/*: Go's
# internal/ rule kept every importer outside the repository out of them.
TOMBSTONES=(pkg framework)

# nested_modules <dir> — the modules go.work names beneath <dir>, one per line:
# modules of their own — a vendor's, a pkg family's —, released by their own
# tags, whose files a tombstone of <dir> keeps.
nested_modules() {
  local dir="$1" v
  for v in "${modules[@]}"; do
    case "$v" in "$dir"/*) echo "$v" ;; esac
  done
}

# sdk_go_directive — the go directive of the SDK's go.mod, which a tombstone
# carries too.
sdk_go_directive() {
  go mod edit -json go.mod | jq -r '.Go // empty'
}

# tombstone_deprecation <sem> — the notice a tombstone's go.mod carries on its
# module directive (ADR 0162): where the module's packages went, and the
# command ADR 0162 §Consumer migration documents to move a go.mod there. Go
# reads a module's deprecation from its latest go.mod — the tombstone's, for
# good —: `go list -m -u` marks the module "(deprecated)" and `go get` prints
# the notice. One line: `go get` prints the first line of a deprecation alone,
# and none of it past 500 bytes (ShortMessage, cmd/go/internal/modload, Go
# 1.27.1); assert_tombstone holds both.
tombstone_deprecation() {
  printf 'moved into %s at %s, import paths unchanged; migrate with: go get %s@%s %s/internal/kernel@none && go mod tidy' \
    "$SDK_MODULE" "$1" "$SDK_MODULE" "$1" "$SDK_MODULE"
}

# tombstone_gomod <dir> <sem> <out> — write to <out> the go.mod of the tombstone
# of the module in <dir>: the deprecation notice on the line right above the
# module directive, where Go reads it, its own module path, the SDK's go
# directive and one requirement, the SDK at <sem>; formatted by `go mod edit`,
# as Go writes it, which keeps the comment where it stands.
tombstone_gomod() {
  local dir="$1" sem="$2" out="$3" goline=""
  goline="$(sdk_go_directive)" || return 1
  if [ -z "$goline" ]; then
    echo "cut-tags: the SDK's go.mod has no go directive for the tombstone of $dir to carry" >&2
    return 1
  fi
  printf '// Deprecated: %s\nmodule %s\n' "$(tombstone_deprecation "$sem")" "$SDK_MODULE/$dir" >"$out" || return 1
  go mod edit -go="$goline" -require="$SDK_MODULE@$sem" "$out"
}

# assert_tombstone <go.mod> <dir> <sem> — fail unless <go.mod> is the
# tombstone's and nothing more: module $SDK_MODULE/<dir>, deprecated — as Go
# reads it — with the notice tombstone_deprecation writes, which `go get`
# prints whole; the SDK's go directive; one requirement, the SDK at <sem>; and
# no other directive.
assert_tombstone() {
  local gomod="$1" dir="$2" sem="$3" json="" goline="" notice="" bytes=0
  goline="$(sdk_go_directive)" || return 1
  notice="$(tombstone_deprecation "$sem")"
  bytes="$(printf '%s' "$notice" | wc -c)"
  if [[ "$notice" == *$'\n'* ]] || [ "$((bytes))" -gt 500 ]; then
    echo "cut-tags: the deprecation notice of the tombstone of $dir is not one line of at most 500 bytes — go get would not print it whole" >&2
    return 1
  fi
  json="$(go mod edit -json "$gomod")" || {
    echo "cut-tags: the tombstone go.mod of $dir does not parse" >&2
    return 1
  }
  if ! jq -e --arg n "$notice" '.Module.Deprecated == $n' >/dev/null <<<"$json"; then
    echo "cut-tags: the tombstone go.mod of $dir does not deprecate $SDK_MODULE/$dir with the migration notice, as Go reads it: '$(jq -r '.Module.Deprecated // ""' <<<"$json")'" >&2
    return 1
  fi
  if ! jq -e --arg m "$SDK_MODULE/$dir" --arg sdk "$SDK_MODULE" --arg v "$sem" --arg go "$goline" '
      .Module.Path == $m and .Go == $go and .Toolchain == null
      and ((.Require // []) | length == 1 and .[0].Path == $sdk and .[0].Version == $v)
      and ([.Replace, .Exclude, .Retract, .Tool, .Ignore, .Godebug] | map((. // []) | length) | add == 0)' \
    >/dev/null <<<"$json"; then
    echo "cut-tags: the tombstone go.mod of $dir is not 'module $SDK_MODULE/$dir; go $goline; require $SDK_MODULE $sem' alone" >&2
    return 1
  fi
}

# tombstone_commit <release-commit> <dir> <sem> — create, and echo, the commit
# the tag <dir>/<sem> names: a child of <release-commit> whose tree is the
# release's with <dir> reduced to its tombstone go.mod. A module nested under
# <dir> keeps its files (framework/connectors/*): it is a module of its own,
# released by its own tags, and Go leaves it out of <dir>'s module anyway; a
# file of <dir> beside it goes. Plumbing only — a temporary index, write-tree,
# commit-tree —, so no hook runs and the worktree is not touched. It runs in a
# command substitution, which errexit does not reach, so every step is checked.
tombstone_commit() {
  local relc="$1" dir="$2" sem="$3" tmp="" blob="" tree="" rec="" path="" n="" keep=0
  local -a nested=()
  tmp="$(mktemp -d)" || return 1
  tombstone_gomod "$dir" "$sem" "$tmp/go.mod" || return 1
  assert_tombstone "$tmp/go.mod" "$dir" "$sem" || return 1
  blob="$(git hash-object -w "$tmp/go.mod")" || return 1
  mapfile -t nested < <(nested_modules "$dir")
  git ls-tree -r -z --full-tree "$relc" >"$tmp/tree" || return 1
  while IFS= read -r -d '' rec; do
    path="${rec#*$'\t'}"
    case "$path" in
      "$dir"/*)
        keep=0
        for n in ${nested[@]+"${nested[@]}"}; do
          case "$path" in "$n"/*)
            keep=1
            break
            ;;
          esac
        done
        if [ "$keep" -eq 0 ]; then continue; fi
        ;;
    esac
    printf '%s\0' "$rec"
  done <"$tmp/tree" >"$tmp/entries" || return 1
  GIT_INDEX_FILE="$tmp/index" git update-index -z --index-info <"$tmp/entries" || return 1
  GIT_INDEX_FILE="$tmp/index" git update-index --add --cacheinfo "100644,$blob,$dir/go.mod" || return 1
  tree="$(GIT_INDEX_FILE="$tmp/index" git write-tree)" || return 1
  git commit-tree "$tree" -p "$relc" \
    -m "release $dir/$sem — tombstone of $SDK_MODULE/$dir: a go.mod that requires $SDK_MODULE $sem, and no package (ADR 0162)" || return 1
  rm -rf "${tmp:?}"
}

# assert_tombstone_tree <commit> <release-commit> <dir> — fail unless <commit>
# is the tombstone it must be: one parent, <release-commit>; under <dir>, the
# modules nested in it aside, one file, <dir>/go.mod; and no change outside
# <dir> nor inside a nested module.
assert_tombstone_tree() {
  local tc="$1" relc="$2" dir="$3" parents="" own="" changed="" p="" n="" hit=0
  local -a nested=()
  mapfile -t nested < <(nested_modules "$dir")
  parents="$(git rev-list --parents -n1 "$tc")"
  if [ "$parents" != "$tc $relc" ]; then
    echo "cut-tags: the tombstone of $dir is not a child of the release commit alone" >&2
    return 1
  fi
  while IFS= read -r p; do
    [ -z "$p" ] && continue
    hit=0
    for n in ${nested[@]+"${nested[@]}"}; do
      case "$p" in "$n"/*)
        hit=1
        break
        ;;
      esac
    done
    if [ "$hit" -eq 0 ]; then own="${own:+$own }$p"; fi
  done < <(git ls-tree -r --name-only "$tc" -- "$dir/")
  if [ "$own" != "$dir/go.mod" ]; then
    echo "cut-tags: the tombstone of $dir holds '$own' under $dir/, not its go.mod alone" >&2
    return 1
  fi
  changed="$(git diff --name-only "$relc" "$tc")"
  while IFS= read -r p; do
    [ -z "$p" ] && continue
    case "$p" in
      "$dir"/*) ;;
      *)
        echo "cut-tags: the tombstone of $dir changed $p, outside $dir/" >&2
        return 1
        ;;
    esac
    for n in ${nested[@]+"${nested[@]}"}; do
      case "$p" in "$n"/*)
        echo "cut-tags: the tombstone of $dir changed $p, in the module $n nested in it" >&2
        return 1
        ;;
      esac
    done
  done <<<"$changed"
}

# publish_release <dir…> — on a detached worktree, rewrite the go.mod of every
# module this release tags to the publishable form, then either (DRY_RUN) print
# the forms + planned tags, or commit on a detached release commit, check that
# no tag of the release exists on origin yet, tag every module at its own
# version and atomic-push. The dev branch is never modified.
publish_release() {
  local d t
  local -a tags=() tombs=()
  for d in "$@"; do tags+=("${NEXT_TAG[$d]}"); done
  # The tombstones go last: on the first root tag after the pkg history alone.
  if [ "$tombstone" -eq 1 ]; then
    for d in "${TOMBSTONES[@]}"; do tombs+=("$d/$root_sem"); done
  fi

  # Validate every tag before touching the repo, each against the shape of its
  # family — lib/tag-format.sh —, and refuse a major past 1: bare module paths
  # carry major 0/1 only; a v2+ release would need /vN module paths (deferred —
  # ADR 0009). Fail loud rather than mint an invalid tag.
  for t in "${tags[@]}"; do
    case "$(version_from_tag "$t")" in
      0.* | 1.*) ;;
      *)
        echo "cut-tags: a release at $t needs /vN module paths (deferred — ADR 0009)" >&2
        return 1
        ;;
    esac
    is_valid_release_tag "$t" || {
      echo "cut-tags: refusing to push malformed release tag '$t'" >&2
      return 1
    }
  done
  for t in ${tombs[@]+"${tombs[@]}"}; do
    is_valid_tombstone_tag "$t" || {
      echo "cut-tags: refusing to push malformed tombstone tag '$t'" >&2
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
    # The SDK's own go.mod is checked whether or not it is tagged: it is the
    # module every other one requires, and a retired requirement in it is the
    # ambiguous import ADR 0162 exists to prevent.
    assert_publishable go.mod
    for d in "$@"; do
      rewrite_publishable "$d/go.mod"
      assert_publishable "$d/go.mod"
    done
    if [ "$DRY_RUN" -eq 1 ]; then
      echo "DRY-RUN: publishable go.mod files (replace dropped, every module of this repository pinned):"
      local shown=0
      for d in "$@"; do
        [ "$d" = "." ] && continue
        shown=1
        echo "  --- $d/go.mod ---"
        grep -nE 'replace|kitsunium/sdk' "$d/go.mod" | sed 's/^/    /' || true
      done
      if [ "$shown" -eq 0 ]; then
        echo "  (no vendor module changed: the SDK's go.mod is published as it is)"
      fi
      if [ "$tombstone" -eq 1 ]; then
        local dry
        dry="$(mktemp -d)"
        echo "DRY-RUN: tombstones of the retired modules for $root_sem (ADR 0162), each a go.mod alone on a child of the release commit:"
        for d in "${TOMBSTONES[@]}"; do
          tombstone_gomod "$d" "$root_sem" "$dry/$d.mod"
          assert_tombstone "$dry/$d.mod" "$d" "$root_sem"
          echo "  --- $d/go.mod ($d/$root_sem) ---"
          sed '/./s/^/    /' "$dry/$d.mod"
          kept="$(nested_modules "$d" | tr '\n' ' ')"
          if [ -n "$kept" ]; then
            echo "    (the modules nested in $d/ keep their files: ${kept% })"
          fi
        done
        rm -rf "${dry:?}"
      fi
      would="${tags[*]}"
      if [ "${#tombs[@]}" -gt 0 ]; then would="$would ${tombs[*]}"; fi
      echo "DRY-RUN: would tag: $would"
    else
      # The race guard, on EACH exact ref, immediately before anything is
      # tagged: a tag of this release that origin already holds was cut by
      # another run since this one read the tags. lib/tag-format.sh compares
      # full refs, since ls-remote matches a pattern against a ref's tail — so
      # v0.1.0 is not answered by pkg/v1/data/v0.1.0 —, and a remote it cannot
      # read is a refusal, never "nobody tagged it".
      local present="" prc=0
      present="$(remote_tags_present origin "${tags[@]}" ${tombs[@]+"${tombs[@]}"})" || prc=$?
      if [ "$prc" -ne 0 ]; then
        echo "cut-tags: origin could not be read before the push — refusing to publish blind" >&2
        exit 2
      fi
      if [ -n "$present" ]; then
        echo "cut-tags: race detected — origin already holds $(tr '\n' ' ' <<<"$present")— another run tagged since this one read the tags; nothing was published" >&2
        exit 2
      fi
      # `core.hooksPath=` (empty) rather than any hooks the clone sets: this
      # commit is the ONE tree in the repository that the pre-commit gates
      # cannot pass, by construction — the rewrite above dropped every module's
      # `replace`, so Go and Bazel can no longer resolve them from disk.
      # `assert_publishable` already checked every go.mod this commit changes.
      #
      # `--allow-empty`: a release that rewrites nothing still needs a release
      # commit whose FIRST PARENT is the main commit it was cut from, because
      # that is where every module's next measurement starts. A tag on the main
      # commit itself would make the next range reopen it.
      local before
      before="$(git rev-parse HEAD)"
      git -c core.hooksPath= commit --quiet --allow-empty -am "release ${tags[0]} — publishable module graph (no replace): ${tags[*]}"
      local relc
      relc="$(git rev-parse HEAD)"
      # A commit that did not happen must never be tagged: the tags would sit on
      # the UNREWRITTEN tree, a go.mod keeping its local `replace` — the `go
      # get` unresolvability ADR 0009 exists to prevent. Observed on a real run.
      if [ "$relc" = "$before" ]; then
        echo "cut-tags: the release commit did not happen — refusing to tag ${tags[*]} at the unrewritten tree" >&2
        exit 1
      fi
      for t in "${tags[@]}"; do git tag -a "$t" -m "release $t" "$relc"; done
      # Each tombstone on its own child of the release commit, checked before
      # it is tagged; then ONE atomic push, so no moment exists where one tag of
      # the release is published and another is not (ADR 0162).
      if [ "$tombstone" -eq 1 ]; then
        local tc
        for d in "${TOMBSTONES[@]}"; do
          tc="$(tombstone_commit "$relc" "$d" "$root_sem")"
          assert_tombstone_tree "$tc" "$relc" "$d"
          git tag -a "$d/$root_sem" -m "release $d/$root_sem — tombstone of $SDK_MODULE/$d (ADR 0162)" "$tc"
        done
      fi
      git push --atomic origin "${tags[@]}" ${tombs[@]+"${tombs[@]}"}
    fi
  )
  rc=$?
  set -e
  git worktree remove --force "$wt" 2>/dev/null || true
  return "$rc"
}

# write_manifest <file> — release-manifest.json: what gives "a release" its
# identity now that it is several modules at several versions (ADR 0170). Its
# `release` is the first tag stdout prints, the one the workflow makes the
# single GitHub release of; `modules` lists every module tagged — directory,
# module path, tag, version, the tag it follows and the size that took it
# there —, `tombstones` the tombstones, `commit` the main commit it was cut
# from and `release_commit` the commit every tag names (empty in a dry run,
# which writes the manifest of the release it would cut).
write_manifest() {
  local file="$1" d rows="[]" relc="" primary="${NEXT_TAG[${selected[0]}]}"
  for d in "${selected[@]}"; do
    rows="$(jq -c --arg dir "$d" --arg module "$(module_path "$d")" --arg tag "${NEXT_TAG[$d]}" \
      --arg version "${NEXT_VERSION[$d]}" --arg previous "${PREVIOUS_TAG[$d]:-}" --arg size "${SIZE_OF[$d]:-}" \
      '. + [{dir: $dir, module: $module, tag: $tag, version: $version, previous: $previous, size: $size}]' <<<"$rows")"
  done
  if [ "$DRY_RUN" -eq 0 ]; then
    relc="$(git rev-parse "refs/tags/$primary^{commit}")"
  fi
  jq -n --arg release "$primary" --arg commit "$(git rev-parse HEAD)" --arg relc "$relc" \
    --argjson dry "$([ "$DRY_RUN" -eq 1 ] && echo true || echo false)" --argjson modules "$rows" \
    '{schema: 1, release: $release, dry_run: $dry, commit: $commit, release_commit: $relc,
      modules: $modules, tombstones: $ARGS.positional}' \
    --args ${tombstone_list[@]+"${tombstone_list[@]}"} >"$file"
}

# ── The cut ──────────────────────────────────────────────────────────────────

# The tags this run reads are origin's: `git fetch --prune --prune-tags` first,
# so a tag deleted there is not read here as a module's last release, and one
# cut there since the checkout is. Only when publishing — a dry run must not
# prune a developer's own tags — and only when there is an origin to publish
# to; one that cannot be read is a refusal, since the base of every module
# would be a guess.
if [ "$DRY_RUN" -eq 0 ] && git remote get-url origin >/dev/null 2>&1; then
  if ! git fetch --quiet --prune --prune-tags origin; then
    echo "cut-tags: git fetch --prune --prune-tags origin failed — refusing to cut a release from tags origin may not hold" >&2
    exit 2
  fi
fi

declare -A NEXT_TAG=() NEXT_VERSION=() PREVIOUS_TAG=() SIZE_OF=()

# The modules this release tags, in go.work's order (`.` first): every one a
# token named, and with `sdk` the SDK module plus every other module that
# changed since its own last tag or was never tagged — each named on stderr
# either way, the only trace a run leaves of why a module was, or was not,
# released.
selected=()
for d in "${modules[@]}"; do
  if in_list "$d" ${explicit[@]+"${explicit[@]}"}; then
    selected+=("$d")
    continue
  fi
  [ "$AUTO" -eq 1 ] || continue
  if [ "$d" = "." ]; then
    selected+=(".")
    continue
  fi
  lt="$(latest_module_tag "$d")"
  if [ -z "$lt" ]; then
    echo "cut-tags: $d has never been tagged — tagged" >&2
    selected+=("$d")
    continue
  fi
  vrc=0
  module_changed "$d" "$lt" || vrc=$?
  case "$vrc" in
    0)
      echo "cut-tags: $d changed since $lt — tagged" >&2
      selected+=("$d")
      ;;
    1) echo "cut-tags: $d unchanged since $lt — keeps it" >&2 ;;
    *) exit 1 ;;
  esac
done

# Each module's version: the next after its own last tag, sized from the merges
# that touched IT (range_size), or --bump for every module, or v0.1.0 for a
# module never tagged — a path no release has published, which is the only path
# allowed to start there (ADR 0170; check-version-floor.sh holds that against
# the proxy). The root keeps ADR 0162's rules: it continues pkg's history, and
# its first tag after it is held and never a patch.
first=0
tombstone=0
root_sem=""
root_last=""
for d in "${selected[@]}"; do
  last="$(latest_module_tag "$d" || true)"
  PREVIOUS_TAG[$d]="$last"
  if [ -z "$last" ]; then
    next="$(module_tag_prefix "$d")v0.1.0"
    SIZE_OF[$d]="new"
    if [ "$d" = "." ]; then first=1; fi
  else
    if [ "$d" = "." ] && ! is_valid_base "$last"; then
      echo "cut-tags: refusing pre-release or malformed: $last" >&2
      exit 1
    fi
    if [ -n "$BUMP" ]; then
      echo "cut-tags: size $BUMP for $([ "$d" = "." ] && echo "the SDK module" || echo "$d") — stated with --bump for the whole range (ADR 0135)" >&2
      size="$BUMP"
    else
      mrange="$(module_range "$d" "$RANGE")" || exit 1
      range_size "$mrange" "$d" || exit 65
      size="$RANGE_SIZE"
    fi
    SIZE_OF[$d]="$size"
    next="$(bump_for "$last" "$size")" || exit 1
    # A pkg/ base IS the first root tag, whatever root tag the repository holds
    # below it. Asking "is there no vX.Y.Z yet?" instead let any stray stable
    # root tag — the v0.0.0 the proxy already knows for this path — lift the
    # hold, and an automatic run cut the module's first release unvalidated.
    if [ "$d" = "." ] && is_valid_pkg_tag "$last"; then
      first=1
      tombstone=1
    fi
  fi
  if ! is_valid_release_tag "$next" || [ "$(module_tag_dir "$next")" != "$d" ]; then
    echo "cut-tags: computed invalid tag '$next' for $d" >&2
    exit 1
  fi
  NEXT_TAG[$d]="$next"
  NEXT_VERSION[$d]="v$(version_from_tag "$next")"
  if [ "$d" = "." ]; then
    root_sem="$next"
    root_last="$last"
  fi
done

tombstone_list=()
if [ "$tombstone" -eq 1 ]; then
  for d in "${TOMBSTONES[@]}"; do tombstone_list+=("$d/$root_sem"); done
fi

# The first root tag moves every consumer from …/pkg to the SDK module (ADR
# 0162): a change a consumer must act on, which v0 says with a minor at least,
# and the proxy keeps whatever version is cut for good. From a pkg/ base a patch
# is therefore refused, like a size nobody decided (ADR 0135, exit 65), naming
# the two ways to state it. It is checked before the hold, so an automatic run
# says so as soon as the merge lands, instead of holding a v0.17.1.
if [ "$tombstone" -eq 1 ] && [ "${SIZE_OF[.]}" = "patch" ]; then
  echo "cut-tags: refusing $root_sem as the SDK module's first tag — after $root_last a patch would hide the module change every consumer makes (ADR 0162)" >&2
  echo "cut-tags:   label the pull request release:minor and re-run this job, or dispatch SDK Release with bump=minor." >&2
  exit 65
fi

# A tombstone tag that already exists names another commit than this release's:
# the tombstone is cut as a new child of a new release commit. It is refused
# before anything is tagged, rather than left to the atomic push, and before the
# hold, so the automatic run that holds the first root tag reports it already.
# A tag the module proxy has fetched is that version for good, so nothing here
# moves or replaces one: a person decides what it is.
if [ "$tombstone" -eq 1 ]; then
  for d in "${TOMBSTONES[@]}"; do
    if git rev-parse -q --verify "refs/tags/$d/$root_sem" >/dev/null; then
      at="$(git rev-parse --short "refs/tags/$d/$root_sem^{}" 2>/dev/null || echo '?')"
      echo "cut-tags: $d/$root_sem already exists, at $at — the tombstone of $SDK_MODULE/$d is cut on this release's own commit, so that tag names another; refusing to publish $root_sem (ADR 0162)" >&2
      echo "cut-tags:   find out who cut it and whether the module proxy has fetched it before anything else is tagged; nothing was published." >&2
      exit 1
    fi
  done
fi

# Bootstrap guard (ADR 0009): the first release of the SDK module publishes its
# go.mod and zip to the proxy and the checksum database PERMANENTLY — the very
# first release, and the first root tag after the pkg history, the first
# release of the module's content (ADR 0162). Its zip and a clean-room `go get`
# must be validated by hand first — so refuse to auto-cut it. A maintainer
# re-runs with --allow-bootstrap once verified. Dry-run is always allowed.
if [ "$first" -eq 1 ] && [ "$DRY_RUN" -eq 0 ] && [ "$ALLOW_BOOTSTRAP" -eq 0 ]; then
  if [ -z "$root_last" ]; then
    echo "cut-tags: refusing to auto-cut the FIRST release ($root_sem). Validate its go.sum + a clean-room 'go get' first (ADR 0009), then re-run with --allow-bootstrap." >&2
  else
    echo "cut-tags: refusing to auto-cut the FIRST release of the SDK module ($root_sem, after $root_last — ADR 0162). Validate its module zip + a clean-room 'go get' first (ADR 0009), then re-run with --allow-bootstrap." >&2
  fi
  exit 3
fi
if [ "$first" -eq 1 ] && [ -n "$root_last" ]; then
  echo "cut-tags: $root_sem is the SDK module's first tag; it continues $root_last (ADR 0162)" >&2
fi
if [ "$tombstone" -eq 1 ]; then
  echo "cut-tags: pkg/$root_sem and framework/$root_sem are tombstones: the last version of $SDK_MODULE/pkg and $SDK_MODULE/framework, which requires $root_sem, provides no package and deprecates its module with the migration command, so an import of either path resolves to the SDK module and go get names the move (ADR 0162)" >&2
fi
for d in "${selected[@]}"; do
  if [ "$d" != "." ]; then
    echo "cut-tags: $d — ${PREVIOUS_TAG[$d]:-never tagged} -> ${NEXT_TAG[$d]}" >&2
  fi
done

# Race window close (plan B3): re-read every module's last tag immediately
# before the publication and abort if another job tagged one in between. The
# exact refs on origin are checked again inside publish_release, after the
# go.mods are rewritten and right before anything is tagged.
for d in "${selected[@]}"; do
  now="$(latest_module_tag "$d" || true)"
  if [ "$now" != "${PREVIOUS_TAG[$d]}" ]; then
    echo "cut-tags: race detected — the last tag of $d moved from ${PREVIOUS_TAG[$d]:-(none)} to ${now:-(none)} during prep; aborting" >&2
    exit 2
  fi
done

publish_release "${selected[@]}"

if [ -n "$MANIFEST" ]; then
  write_manifest "$MANIFEST"
fi

# stdout is the list of tags pushed, in go.work's order — the SDK's first when
# it is cut —, then the tombstones. Its first line is the release the workflow
# makes the ONE GitHub release of; release-notes.sh classifies the rest.
if [ "$DRY_RUN" -eq 0 ]; then
  for d in "${selected[@]}"; do
    echo "${NEXT_TAG[$d]}"
  done
  for t in ${tombstone_list[@]+"${tombstone_list[@]}"}; do
    echo "$t"
  done
fi
