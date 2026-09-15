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
    # stderr of the query, kept out of the caller's stream until we know whether
    # it describes a failure. One file for the whole loop; the trap covers the
    # loud-exit path below, which does not fall through to the cleanup.
    rdeps_err="$(mktemp)"
    trap 'rm -f "$rdeps_err"' EXIT
    for modpath in "${changed_internal[@]}"; do
      [ -z "$modpath" ] && continue
      # A path that is not a Bazel subtree is not a failed query — it is a path
      # with nothing to ask about, and it must not turn into a red release.
      #
      # This case is REAL, not defensive: the awk above reduces a changed file to
      # `internal/<second-component>`, which is the right reduction for
      # `internal/<mod>/go.mod` but leaves a file that is ALREADY at depth two
      # intact. `internal/CLAUDE.md` is such a file and appears in ordinary
      # commits, so the bogus label `//internal/CLAUDE.md/...` reaches the query.
      # Measured on this repository:
      #
      #   rdeps(//pkg/..., //internal/service/...)    exit 0, 229 lines
      #   rdeps(//pkg/..., //internal/CLAUDE.md/...)  exit 7, 0 lines,
      #       stderr: ERROR: no targets found beneath 'internal/CLAUDE.md'
      #
      # The filter is structural — does any BUILD.bazel exist beneath the path —
      # rather than a match on that error sentence, because the sentence is
      # Bazel's to reword and a version that rephrases it would silently turn
      # every CLAUDE.md commit into a failed release.
      if [ -z "$(find "$modpath" -name BUILD.bazel -print -quit 2>/dev/null)" ]; then
        continue
      fi
      # Capture the ANSWER and the EXIT CODE separately. Both halves matter, and
      # the previous shape had neither: `2>/dev/null` threw the error away and
      # the exit status was never consulted, so a query that FAILED and a query
      # that did not reach //pkg/... produced the same silence and the same
      # verdict — "no release". That is how c707de5 (#214) landed on main with
      # SDK Release reporting success and no tag cut (#227). The comment this
      # replaces records the SAME verdict being inverted once before, by
      # `bazel query … | grep -q .` under `pipefail`; that FORM was fixed and the
      # blindness above it was not.
      #
      # Command substitution rather than a pipe or a process substitution: it
      # reads to EOF, so the 64 KiB pipe buffer that made `grep -q .` deadly here
      # is not in the picture, and no SIGPIPE can reach bazel to manufacture an
      # exit code it never chose.
      rdeps_out=""
      rdeps_rc=0
      rdeps_out="$(bazel query "rdeps(//pkg/..., //${modpath}/...)" 2>"$rdeps_err")" || rdeps_rc=$?
      # LOUD. A release that cannot be computed must not be rendered as a release
      # that is not needed. The workflow step runs this with no `|| true`
      # precisely so a non-zero exit fails the job instead of becoming a silent
      # no-op, and defect 2 of #227 is that a skipped tag step still reports
      # success — so this refusal is the only thing that makes the failure
      # visible at all.
      if [ "$rdeps_rc" -ne 0 ]; then
        echo "compute-bumps.sh: bazel query failed (exit ${rdeps_rc}) for //${modpath}/..." >&2
        echo "compute-bumps.sh: refusing to report 'no release' for a computation that did not run" >&2
        cat "$rdeps_err" >&2
        exit 1
      fi
      # Query ran. Empty means this module genuinely does not reach //pkg/... —
      # a measured absence, which is a different thing from the two above.
      # `case` on the variable, never `grep -c` (which prints 0 AND exits 1, a
      # combination that has inverted verdicts in this repository before).
      case "$rdeps_out" in
        "") continue ;;
        *) need_bump=1; break ;;
      esac
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
