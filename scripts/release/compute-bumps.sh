#!/usr/bin/env bash
# scripts/release/compute-bumps.sh — emit the list of `pkg/vN` majors
# whose docs/code changed since the previous release tag and therefore
# need a patch bump.
#
# Decision matrix (plan v2 §4, ADR 0007):
#   change under pkg/<major>/**      -> bump <major>
#   change under internal/**         -> bump every <major> whose
#                                       bazel rdeps depend on it
#   no relevant change               -> emit nothing (exit 0)
#
# Output: one major per line ("v1", "v2", ...). Sorted, deduped.
# Stable contract — consumed by cut-tags.sh and CI.

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
compute-bumps.sh — list pkg/<major> dirs needing a patch bump.

Usage: $0 [--dry-run] [--range=<rev>..HEAD]

Without --range, infers from the last tag or, on a bootstrap repo,
falls back to the root commit. Shallow-clone safe.
EOF
      exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 64 ;;
  esac
done

# Shallow-clone safe range (plan B2). git describe fails silently on
# repos without tags; fall back to the root commit (always present).
if [ -z "$RANGE" ]; then
  if last_tag="$(git describe --tags --abbrev=0 2>/dev/null)"; then
    RANGE="${last_tag}..HEAD"
  elif [ "$(git rev-list --count HEAD)" -eq 1 ]; then
    # Single-commit repo: root == HEAD, so root..HEAD is empty and the
    # initial commit's paths would be invisible. Diff against the empty
    # tree so the first release sees every added path.
    RANGE="$(git hash-object -t tree /dev/null)..HEAD"
  else
    root="$(git rev-list --max-parents=0 HEAD | head -n1)"
    RANGE="${root}..HEAD"
  fi
fi

# Empty diff is a legitimate no-op (merge commit, docs-only push).
declare -A bump

# 1. Direct pkg/<major>/ changes. Process substitution keeps the
# associative array alive in the parent shell (plan B1).
while IFS= read -r path; do
  case "$path" in
    pkg/v*/*)
      m="$(awk -F/ '{print $2}' <<<"$path")"
      [ -n "$m" ] && bump["$m"]=1
      ;;
  esac
done < <(git diff --name-only "$RANGE" || true)

# 2. internal/* changes - rdeps per package, not per file. Bazel is
# invoked once per touched internal package, never once per file.
mapfile -t changed_internal < <(
  git diff --name-only "$RANGE" 2>/dev/null \
    | awk -F/ '/^internal\//{print $1"/"$2"/"$3}' \
    | sort -u
)

if [ "${#changed_internal[@]}" -gt 0 ] && command -v bazel >/dev/null 2>&1; then
  for major_dir in pkg/v*; do
    [ -d "$major_dir" ] || continue
    major="$(basename "$major_dir")"
    for pkgpath in "${changed_internal[@]}"; do
      [ -z "$pkgpath" ] && continue
      if bazel query "rdeps(//${major_dir}/..., //${pkgpath}/...)" 2>/dev/null | grep -q .; then
        bump["$major"]=1
      fi
    done
  done
fi

# 3. Emit sorted majors. Dry-run echoes the same payload to stdout, no
# side effects either way (compute is pure).
if [ "${#bump[@]}" -eq 0 ]; then
  exit 0
fi
for k in "${!bump[@]}"; do echo "$k"; done | sort
if [ "$DRY_RUN" -eq 1 ]; then
  echo "(dry-run: $RANGE)" >&2
fi
