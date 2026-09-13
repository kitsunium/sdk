#!/usr/bin/env bash
# scripts/release/compute-bumps.sh — decide whether the single public module
# `pkg` (github.com/kitsunium/sdk/pkg) needs a patch bump since the previous
# release tag, and emit the token `pkg` if so.
#
# Decision matrix (ADR 0007, updated for the bare-`pkg` module — ADR 0009):
#   change under pkg/v*/** or pkg/go.mod  -> bump pkg
#   change under internal/**              -> bump pkg iff its bazel rdeps
#                                            reach //pkg/...
#   no relevant change                    -> emit nothing (exit 0)
#
# Output: the literal token "pkg" on a single line, or nothing. (Before the
# bare-`pkg` migration this emitted one "vN" major per line; there is now a
# single public module, so there is a single token.) Stable contract —
# consumed by cut-tags.sh and CI.

set -euo pipefail
shopt -s nullglob

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"

DRY_RUN=0
RANGE=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --range=*) RANGE="${arg#--range=}" ;;
    --help|-h)
      cat <<EOF
compute-bumps.sh — emit "pkg" if the public module needs a patch bump.

Usage: $0 [--dry-run] [--range=<rev>..HEAD]

Without --range, infers from the last tag or, on a bootstrap repo,
falls back to the root commit. Shallow-clone safe.
EOF
      exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 64 ;;
  esac
done

# Range to diff for "what changed since the last release". The baseline comes
# from release_base() in the shared lib rather than from `git describe`, for the
# reason spelled out there (the release commit is detached, so HEAD cannot
# describe it) — and it is shared with cut-tags.sh so the half that decides
# WHETHER to release and the half that decides HOW BIG read the same commits
# (ADR 0085).
if [ -z "$RANGE" ]; then
  base="$(release_base)"
  if [ -n "$base" ]; then
    RANGE="${base}..HEAD"
  else
    # No baseline: root == HEAD, so root..HEAD is empty and the initial commit's
    # paths would be invisible. Diff against the empty tree instead so the first
    # release sees every added path.
    RANGE="$(git hash-object -t tree /dev/null)..HEAD"
  fi
fi

need_bump=0

# 1. Direct public-module changes: source under pkg/v*/ or the module file
# pkg/go.mod. (BUILD.bazel / CLAUDE.md churn alone does not warrant a release.)
while IFS= read -r path; do
  case "$path" in
    # Maintainer-only metadata that ships in the module zip but carries no
    # consumer-visible change — its churn alone must not cut a release.
    */CLAUDE.md|*/BUILD.bazel) continue ;;
    pkg/v*/*|pkg/go.mod) need_bump=1; break ;;
  esac
done < <(git diff --name-only "$RANGE" || true)

# 2. internal/* changes — rdeps at the MODULE root. A changed file is reduced to
# its module dir (internal/<mod>), so a go.mod / go.sum change maps to a valid
# Bazel subtree //internal/<mod>/... rather than a bogus //internal/<mod>/go.mod/...
# label (which fails the query and would silently drop the bump). If any reaches
# //pkg/..., the public module must re-release.
if [ "$need_bump" -eq 0 ]; then
  mapfile -t changed_internal < <(
    git diff --name-only "$RANGE" 2>/dev/null \
      | awk -F/ '/^internal\//{print $1"/"$2}' \
      | sort -u
  )
  if [ "${#changed_internal[@]}" -gt 0 ] && command -v bazel >/dev/null 2>&1; then
    for modpath in "${changed_internal[@]}"; do
      [ -z "$modpath" ] && continue
      if bazel query "rdeps(//pkg/..., //${modpath}/...)" 2>/dev/null | grep -q .; then
        need_bump=1
        break
      fi
    done
  fi
fi

# 3. Emit the single token. Dry-run echoes the same payload, no side effects.
if [ "$need_bump" -eq 0 ]; then
  exit 0
fi
echo "pkg"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "(dry-run: $RANGE)" >&2
fi
