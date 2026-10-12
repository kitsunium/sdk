#!/usr/bin/env bash
# scripts/release/check-version-floor.sh — no module path is ever tagged at or
# below the highest version the module proxy lists for it (ADR 0171 §2).
#
# The proxy and the checksum database keep every published version for good: a
# version re-cut is refused by `go` for anyone who fetched the first one, and a
# version cut below the published maximum is invisible to `@latest`. Only a path
# the proxy has never served may start at v0.1.0. This script asks the proxy
# rather than the clone, because the clone is exactly what can be behind: a tag
# deleted locally, a fetch that missed one, a branch cut before a release.
#
# Two questions, one rule:
#
#   (no tag given)  for every module go.work names, its latest LOCAL tag must be
#                   at or above the proxy's maximum — the release scripts cut
#                   the next version above that tag, so the next cut is then
#                   above the proxy's too —, and a module never tagged locally
#                   must be a path the proxy has never served, since the scripts
#                   would start it at v0.1.0.
#   <tag…>          each tag given (or listed by --manifest, or one per line on
#                   stdin with `-`) must be strictly above the proxy's maximum
#                   for its module.
#
# The proxy is `--proxy=<url>`, else $VERSION_FLOOR_PROXY, else
# https://proxy.golang.org. A `file://<dir>` proxy reads `<dir>/<module>/@v/list`
# — GOPROXY's own file layout — which is how the suite and the rehearsal run
# with no network. A 404 or 410 answer is "never published"; any other failure
# is exit 2, never read as "never published", because that reading is the one
# that lets a v0.1.0 through over a published v0.19.7.
#
# Exit 0: every module clears its floor. 1: one does not (each named on
# stderr). 2: the proxy could not be read. 64: bad usage.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"
# shellcheck source=lib/release-modules.sh
. "$here/lib/release-modules.sh"

PROXY="${VERSION_FLOOR_PROXY:-https://proxy.golang.org}"
MANIFEST=""
WORK="go.work"
tags=()
for arg in "$@"; do
  case "$arg" in
    --proxy=*) PROXY="${arg#--proxy=}" ;;
    --manifest=*) MANIFEST="${arg#--manifest=}" ;;
    --go-work=*) WORK="${arg#--go-work=}" ;;
    -)
      while IFS= read -r t; do
        [ -n "$t" ] && tags+=("$t")
      done
      ;;
    -h | --help)
      sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*)
      echo "check-version-floor: unknown argument $arg" >&2
      exit 64
      ;;
    *) tags+=("$arg") ;;
  esac
done
PROXY="${PROXY%/}"

if [ -n "$MANIFEST" ]; then
  if [ ! -r "$MANIFEST" ]; then
    echo "check-version-floor: $MANIFEST is missing or unreadable" >&2
    exit 64
  fi
  while IFS= read -r t; do
    [ -n "$t" ] && tags+=("$t")
  done < <(jq -r '(.modules[]?.tag), (.tombstones[]?)' "$MANIFEST")
fi

# escape_module <path> — the proxy's case encoding: an upper-case letter is `!`
# and its lower case (go.dev/ref/mod#goproxy-protocol).
escape_module() {
  sed -E 's/([A-Z])/!\L\1/g' <<<"$1"
}

# published_max <module> — the highest version the proxy lists for <module>,
# empty when it has never served one; exit 2 when the proxy cannot be read.
published_max() {
  local m path list="" code="" tmp=""
  m="$(escape_module "$1")"
  case "$PROXY" in
    file://*)
      path="${PROXY#file://}/$m/@v/list"
      if [ -r "$path" ]; then list="$(cat "$path")"; fi
      ;;
    *)
      tmp="$(mktemp)"
      code="$(curl -sS --max-time 30 -o "$tmp" -w '%{http_code}' "$PROXY/$m/@v/list")" || {
        rm -f "$tmp"
        echo "check-version-floor: $PROXY could not be reached for $1" >&2
        return 2
      }
      case "$code" in
        200) list="$(cat "$tmp")" ;;
        404 | 410) list="" ;;
        *)
          rm -f "$tmp"
          echo "check-version-floor: $PROXY answered HTTP $code for $1 — refusing to read that as 'never published'" >&2
          return 2
          ;;
      esac
      rm -f "$tmp"
      ;;
  esac
  awk 'NF' <<<"$list" | semver_sort | awk 'END { if (NR) print }'
}

# semver_sort — stdin: versions vX.Y.Z(-pre)?, one per line. stdout: ascending,
# a pre-release below its release (semver §11, its identifiers compared as
# strings — enough for an order among one module's tags).
semver_sort() {
  awk '{
    v = $0; sub(/^v/, "", v)
    pre = ""; i = index(v, "-"); if (i) { pre = substr(v, i + 1); v = substr(v, 1, i - 1) }
    split(v, p, ".")
    printf "%d\t%d\t%d\t%d\t%s\t%s\n", p[1], p[2], p[3], (pre == "" ? 1 : 0), pre, $0
  }' | LC_ALL=C sort -t "$(printf '\t')" -k1,1n -k2,2n -k3,3n -k4,4n -k5,5 | cut -f6
}

# above <a> <b> — 0 when version <a> is strictly above <b>.
above() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | semver_sort | tail -n1)" = "$1" ]
}

fail=0
checked=0

# check_tag <tag> — the tag's version is strictly above its module's published
# maximum.
check_tag() {
  local tag="$1" dir module ver max rc=0
  if ! dir="$(module_tag_dir "$tag")"; then
    if is_valid_tombstone_tag "$tag"; then
      dir="${tag%/v*}"
    else
      echo "check-version-floor: '$tag' is no tag a release cuts" >&2
      fail=1
      return
    fi
  fi
  module="$(module_path "$dir")"
  ver="v$(version_from_tag "$tag")"
  max="$(published_max "$module")" || rc=$?
  [ "$rc" -eq 0 ] || exit 2
  checked=$((checked + 1))
  if [ -z "$max" ]; then
    echo "ok $tag — $module was never published"
  elif above "$ver" "$max"; then
    echo "ok $tag — above $module $max"
  else
    echo "check-version-floor: $tag is at or below $max, the highest version the proxy lists for $module — a published version is never reused nor gone below (ADR 0171)" >&2
    fail=1
  fi
}

# check_module <dir> — the module's latest local tag is at or above the proxy's
# maximum, or, never tagged here, the proxy never served it.
check_module() {
  local dir="$1" module last ver max rc=0
  module="$(module_path "$dir")"
  last="$(latest_module_tag "$dir" || true)"
  max="$(published_max "$module")" || rc=$?
  [ "$rc" -eq 0 ] || exit 2
  checked=$((checked + 1))
  if [ -z "$last" ]; then
    if [ -n "$max" ]; then
      echo "check-version-floor: $module has no tag in this clone, and the proxy lists $max — its next release would start at v0.1.0, below it; fetch the tags (ADR 0171)" >&2
      fail=1
    else
      echo "ok $dir — never published, starts at v0.1.0"
    fi
    return
  fi
  ver="v$(version_from_tag "$last")"
  if [ -n "$max" ] && above "$max" "$ver"; then
    echo "check-version-floor: $module's latest tag here is $last, below $max, which the proxy lists — the next release would be cut at or below a published version; fetch the tags (ADR 0171)" >&2
    fail=1
  else
    if [ -n "$max" ]; then
      echo "ok $dir — $last, the highest the proxy lists is $max"
    else
      echo "ok $dir — $last, never published"
    fi
  fi
}

if [ "${#tags[@]}" -gt 0 ]; then
  for t in "${tags[@]}"; do check_tag "$t"; done
else
  modules=""
  modules="$(release_modules "$WORK")" || exit 64
  while IFS= read -r d; do
    [ -n "$d" ] && check_module "$d"
  done <<<"$modules"
fi

if [ "$checked" -eq 0 ] && [ "$fail" -eq 0 ]; then
  echo "check-version-floor: nothing was checked — refusing to report a floor nobody measured" >&2
  exit 1
fi
exit "$fail"
