#!/usr/bin/env bash
# scripts/release/lib/release-modules.sh — the modules a release may tag, and
# what each of them changed since its own last tag (ADR 0171). Sourced by
# compute-bumps.sh, which decides WHICH modules release, and cut-tags.sh, which
# sizes and tags each of them — after lib/tag-format.sh and
# lib/release-scope.sh, whose functions it calls.
#
# A release is no longer one module. Every module go.work names — the SDK at
# the root, a pkg family, the shared internal module, a vendor module — is
# measured on its OWN: from the commit its own last tag was cut from, over its
# own directory, the modules nested in that directory left out. So a change to
# one family releases that family and nothing else, and a label on its pull
# request sizes that family and nothing else.
#
# Everything here reads to EOF and reports a git that failed as exit 2, never
# as "nothing changed": an absence of measurement read as an absence of change
# is the defect #226 and #227 were about.

set -euo pipefail

# release_modules [go.work] — every module a release may tag, one directory per
# line: `.` first, then the others sorted. Read from go.work, so a module added
# to the workspace is released without anyone editing a list (ADR 0137's census
# argument, applied to tags — ADR 0147 §9).
#
# It REFUSES rather than guesses (exit 1, the reason on stderr): a missing or
# unreadable go.work, one that does not use `.`, and one that uses a directory
# no release can tag — `./pkg`, `./framework` or `./internal/core`, the modules
# ADR 0162 retired, whose packages are another module's, so a consumer
# requiring both would meet an ambiguous import. What may be tagged is what
# lib/tag-format.sh has a shape for (is_module_dir): the root, `pkg/v1/<family>`,
# `internal`, `third-party/<path>` and `framework/connectors/<engine>`.
release_modules() {
  local work="${1:-go.work}" dirs="" rc=0 d bad="" has_root=0
  if [ ! -r "$work" ]; then
    echo "release_modules: $work is missing or unreadable — refusing to guess the release's modules" >&2
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
    echo "release_modules: could not parse $work (exit $rc)" >&2
    return 1
  fi
  while IFS= read -r d; do
    [ -z "$d" ] && continue
    if [ "$d" = "." ]; then
      has_root=1
      continue
    fi
    if ! is_module_dir "$d"; then
      bad="${bad:+$bad }$d"
    fi
  done <<<"$dirs"
  if [ "$has_root" -eq 0 ]; then
    echo "release_modules: $work does not use . — refusing a release without the SDK module (ADR 0162)" >&2
    return 1
  fi
  if [ -n "$bad" ]; then
    echo "release_modules: $work uses $bad, which no release can tag — a module lives at the root, under pkg/v1/<family>, at internal, under third-party/ or under framework/connectors/ (ADR 0171); pkg, framework and a layer of internal are retired (ADR 0162)" >&2
    return 1
  fi
  echo "."
  awk 'NF && $0 != "."' <<<"$dirs" | LC_ALL=C sort -u
}

# module_path <dir> — the Go module path of the module in <dir>.
module_path() {
  if [ "$1" = "." ]; then
    echo "github.com/kitsunium/sdk"
  else
    echo "github.com/kitsunium/sdk/$1"
  fi
}

# module_pathspec <dir> <module-list> — the git pathspec of the module in <dir>,
# one element per line: its directory, then an exclusion for every module of
# <module-list> (newline-separated) nested in it. The root's directory is the
# whole tree, so every other module is nested in it.
module_pathspec() {
  local dir="$1" list="$2" m
  if [ "$dir" = "." ]; then echo "."; else echo "$dir/"; fi
  while IFS= read -r m; do
    [ -z "$m" ] && continue
    [ "$m" = "$dir" ] && continue
    [ "$m" = "." ] && continue
    if [ "$dir" = "." ]; then
      echo ":(exclude)$m/"
    else
      case "$m" in "$dir"/*) echo ":(exclude)$m/" ;; esac
    fi
  done <<<"$list"
}

# module_base <dir> — the commit the last release of the module in <dir> was cut
# FROM: its latest tag's first parent (lib/tag-format.sh, release_base's reason).
# Empty, exit 0, when the module has never been tagged; exit 1 when its latest
# tag names no commit with a parent.
module_base() {
  local dir="$1" last=""
  last="$(latest_module_tag "$dir")" || return 1
  [ -n "$last" ] || return 0
  if [ "$dir" = "." ]; then
    # The root's base is release_base's, which also knows a bootstrap repository.
    release_base
    return
  fi
  tag_base "$last" || {
    echo "module_base: $last names no commit with a parent" >&2
    return 1
  }
}

# module_range <dir> [range] — the range the module in <dir> is measured over:
# <range> when given (a caller's --range applies to every module), else
# <base>..HEAD from module_base. A module never tagged has no "since": its range
# starts at the empty tree, so every path it holds is a change — and a module
# holding nothing that counts is still not released. That range is a diff's,
# never a walk's: `git log` cannot start at a tree, and a new module is never
# sized (it starts at v0.1.0).
module_range() {
  local dir="$1" range="${2:-}" base=""
  if [ -n "$range" ]; then
    echo "$range"
    return 0
  fi
  base="$(module_base "$dir")" || return 1
  if [ -n "$base" ]; then
    echo "$base..HEAD"
  else
    echo "$(git hash-object -t tree /dev/null)..HEAD"
  fi
}

# is_new_range <range> — 0 when <range> starts at the empty tree: the range of a
# module never tagged.
is_new_range() {
  [ "${1%%..*}" = "$(git hash-object -t tree /dev/null)" ]
}

# module_paths <dir> <range> <module-list> — the paths of the module in <dir>
# that changed over <range>, one per line, the modules nested in it left out.
# Exit 2, with the reason on stderr, when git cannot answer.
module_paths() {
  local dir="$1" range="$2" list="$3" out="" rc=0 a b
  local -a spec=()
  mapfile -t spec < <(module_pathspec "$dir" "$list")
  case "$range" in
    *...*) a="${range%%...*}" b="${range#*...}" ;;
    *..*) a="${range%%..*}" b="${range#*..}" ;;
    *)
      echo "module_paths: '$range' is no <a>..<b> range" >&2
      return 2
      ;;
  esac
  out="$(git diff --name-only "$a" "${b:-HEAD}" -- "${spec[@]}")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "module_paths: git diff over '$range' for $dir failed (exit $rc) — refusing to guess what it changed" >&2
    return 2
  fi
  if [ -n "$out" ]; then printf '%s\n' "$out"; fi
}

# commit_touches_module <sha> <dir> <module-list> — 0 when the first-parent
# commit <sha> changed a path of the module in <dir> that can cut a release
# (lib/release-scope.sh); 1 when none; 2 when git cannot answer.
#
# A DIFF against the commit's first parent, never `git log -1 <sha> -- <path>`:
# with a pathspec, log simplifies history and answers the newest commit at or
# below <sha> that touched the path — so a merge that changed pkg/v1/data was
# read as touching pkg/v1/net through an older commit, and its label sized both
# (seen red in test-modules.bats first). The first parent is also what a true
# merge must be read against: a plain --name-only reports no file for one
# (ADR 0085). A root commit is diffed against the empty tree.
commit_touches_module() {
  local sha="$1" dir="$2" list="$3" out="" rc=0 parent=""
  local -a spec=()
  mapfile -t spec < <(module_pathspec "$dir" "$list")
  parent="$(git rev-parse -q --verify "$sha^1" 2>/dev/null)" || parent="$(git hash-object -t tree /dev/null)"
  out="$(git diff --name-only "$parent" "$sha" -- "${spec[@]}")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "commit_touches_module: git log for $sha failed (exit $rc)" >&2
    return 2
  fi
  release_scope_any <<<"$out"
}
