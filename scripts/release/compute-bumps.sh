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
# Maintainer-only metadata is dropped from the changed-path list BEFORE either
# rule sees it, by the one predicate in lib/release-scope.sh (ADR 0089). Both
# rules used to be blind to it in different ways: rule 1 carried its own
# two-name list and let `pkg/v1/errs/BENCH.md` cut pkg/v0.4.4 from a wholly
# documentary diff (#238), and rule 2 never applied any exclusion at all —
# it reduces a path to its module dir first, at which point the file's identity
# is gone (#220). Filtering once, up front, is what makes the two agree.
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
# shellcheck source=lib/release-scope.sh
. "$here/lib/release-scope.sh"

DRY_RUN=0
EXPLAIN=0
REQUIRE_BAZEL=0
RANGE=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --explain) EXPLAIN=1 ;;
    --require-bazel) REQUIRE_BAZEL=1 ;;
    --range=*) RANGE="${arg#--range=}" ;;
    --help|-h)
      cat <<EOF
compute-bumps.sh — emit "pkg" if the public module needs a patch bump.

Usage: $0 [--dry-run] [--explain] [--require-bazel] [--range=<rev>..HEAD]

Without --range, infers from the last tag or, on a bootstrap repo,
falls back to the root commit. Shallow-clone safe.

--explain writes the verdict and the reason for it to stderr. stdout stays
the stable contract ("pkg" or nothing), so a caller that parses it is
unaffected. It is opt-in rather than always-on because the BATS suite
merges the two streams into one assertion.

--require-bazel refuses, instead of skipping rule 2, when bazel is absent
and internal/ module dirs changed. The release lane passes it; a developer
without bazel does not, and still gets rule 1.
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

# explain <line…> — the reason, on stderr, only when asked. #226 is that an
# internal/-only change publishes nothing and "the log cannot say why": every
# outcome below now names itself, so a run page distinguishes "nothing to
# publish" from "the computation did not reach a verdict". stdout is untouched.
explain() {
  [ "$EXPLAIN" -eq 1 ] || return 0
  printf 'compute-bumps.sh: %s\n' "$@" >&2
}

# The changed paths, ONCE. It used to be two `git diff` calls, one per rule,
# each ending in `|| true` or `2>/dev/null` — so a git that failed read as "no
# paths changed", which is "no release". That is the same shape as the rdeps
# blindness #227 was opened for, in the rule above it, and it was never
# reported because the outcome is plausible. A range git cannot walk is now a
# refusal, symmetric with cut-tags.sh's own check on the same range.
changed_raw=""
changed_rc=0
changed_raw="$(git diff --name-only "$RANGE")" || changed_rc=$?
if [ "$changed_rc" -ne 0 ]; then
  echo "compute-bumps.sh: git diff --name-only '$RANGE' failed (exit ${changed_rc})" >&2
  echo "compute-bumps.sh: refusing to report 'no release' for a range that could not be read" >&2
  exit 1
fi

# Maintainer-only metadata dropped here and nowhere else, by the one predicate
# both halves of a release share (lib/release-scope.sh, ADR 0089).
#
# Through a command substitution whose status is CHECKED, not a process
# substitution into `mapfile`. A classifier that dies reads as "no path counts",
# which reads as "no release" — the same substitution of absence-of-measurement
# for absence-of-result that #226 and #227 are about, one layer further in. A
# `mapfile < <(…)` cannot report it: mapfile's status is its own.
#
# `printf '%s\n'` and not `printf '%s'`: command substitution strips the
# trailing newline, and `while read` does not process a final line that has
# none — so a range whose diff is ONE path counted as zero.
changed_count=0
counting_count=0
counting_raw=""
counting_rc=0
counting_raw="$(printf '%s\n' "$changed_raw" | release_scope_filter)" || counting_rc=$?
if [ "$counting_rc" -ne 0 ]; then
  echo "compute-bumps.sh: the release-scope filter failed (exit ${counting_rc})" >&2
  echo "compute-bumps.sh: refusing to report 'no release' for a path list that could not be classified" >&2
  exit 1
fi
counting=()
# Guarded: `printf '%s\n' ""` emits one EMPTY line, which mapfile would read as
# a one-element array and count as one path that counts.
if [ -n "$counting_raw" ]; then
  mapfile -t counting < <(printf '%s\n' "$counting_raw")
fi
while IFS= read -r path; do
  [ -z "$path" ] && continue
  changed_count=$((changed_count + 1))
done < <(printf '%s\n' "$changed_raw")
counting_count="${#counting[@]}"

explain "range: $RANGE"
explain "changed paths: ${changed_count} (${counting_count} could carry a consumer-visible change)"
if [ "$changed_count" -gt 0 ] && [ "$counting_count" -eq 0 ]; then
  explain "every changed path is maintainer-only metadata (*.md except README.md, BUILD.bazel)"
fi

# 1. Direct public-module changes: source under pkg/v*/ or the module file
# pkg/go.mod. The list is already free of maintainer-only metadata.
for path in ${counting[@]+"${counting[@]}"}; do
  case "$path" in
    pkg/v*/*|pkg/go.mod) need_bump=1; explain "rule 1: $path is a public-module change -> bump"; break ;;
  esac
done

# 2. internal/* changes — rdeps at the MODULE root. A changed file is reduced to
# its module dir (internal/<mod>), so a go.mod / go.sum change maps to a valid
# Bazel subtree //internal/<mod>/... rather than a bogus //internal/<mod>/go.mod/...
# label (which fails the query and would silently drop the bump). If any reaches
# //pkg/..., the public module must re-release.
if [ "$need_bump" -eq 0 ]; then
  # Reduced from the FILTERED list, not from a second `git diff`. Rule 2 had no
  # exclusion of its own and could not have had one usefully: it reduces a path
  # to `internal/<mod>` before asking anything, and after that reduction a
  # BENCH.md and a .go file are the same module dir. #237's eight internal
  # BENCH.md files reached the query for exactly this reason.
  internal_raw=""
  internal_rc=0
  internal_raw="$(
    printf '%s\n' ${counting[@]+"${counting[@]}"} \
      | awk -F/ '/^internal\//{print $1"/"$2}' \
      | sort -u
  )" || internal_rc=$?
  if [ "$internal_rc" -ne 0 ]; then
    echo "compute-bumps.sh: reducing the changed paths to module dirs failed (exit ${internal_rc})" >&2
    echo "compute-bumps.sh: refusing to skip the rdeps rule for a list that could not be built" >&2
    exit 1
  fi
  changed_internal=()
  if [ -n "$internal_raw" ]; then
    mapfile -t changed_internal < <(printf '%s\n' "$internal_raw")
  fi
  if [ "${#changed_internal[@]}" -eq 0 ]; then
    explain "rule 2: no internal/ module dir in the filtered path list — nothing to query"
  elif ! command -v bazel >/dev/null 2>&1; then
    # The `command -v bazel` guard below is #227 defect 1 wearing a different
    # hat: a missing bazel and an rdeps set that reaches nothing produce the
    # same empty stdout and the same verdict, "no release". d625975's 0 s step 6
    # in #227 is what that looks like from the outside, and it is green.
    #
    # A developer without bazel should still get rule 1, so the default stays a
    # skip — but it is now announced, and a caller for whom rule 2 is
    # load-bearing (the release lane) passes --require-bazel and gets a refusal.
    if [ "$REQUIRE_BAZEL" -eq 1 ]; then
      echo "compute-bumps.sh: bazel is not on PATH and --require-bazel was given" >&2
      echo "compute-bumps.sh: ${#changed_internal[@]} internal module dir(s) would go unmeasured; refusing to report 'no release'" >&2
      exit 1
    fi
    explain "rule 2: bazel is not on PATH — ${#changed_internal[@]} internal module dir(s) went UNMEASURED"
  fi
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
        explain "rule 2: //${modpath}/... is not a Bazel subtree (no BUILD.bazel beneath it) — skipped, not queried"
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
        "")
          explain "rule 2: rdeps(//pkg/..., //${modpath}/...) ran and reached nothing — a measured absence"
          continue
          ;;
        *)
          explain "rule 2: rdeps(//pkg/..., //${modpath}/...) reaches //pkg/... -> bump"
          need_bump=1
          break
          ;;
      esac
    done
  fi
fi

# 3. Emit the single token. Dry-run echoes the same payload, no side effects.
if [ "$need_bump" -eq 0 ]; then
  explain "verdict: NO RELEASE (nothing consumer-visible changed in this range)"
  exit 0
fi
explain "verdict: RELEASE (token 'pkg')"
echo "pkg"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "(dry-run: $RANGE)" >&2
fi
