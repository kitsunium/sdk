#!/usr/bin/env bash
# scripts/release/compute-bumps.sh — decide WHICH modules need a release: emit,
# one per line, the directory of every module go.work names that changed, since
# its OWN last tag, in a path that can carry a consumer-visible change — `.` for
# the SDK module at the root (ADR 0162), `pkg/v1/<family>` for a pkg family,
# `internal` for the shared internal module (ADR 0170), `third-party/<path>` and
# `framework/connectors/<engine>` for a module that requires a vendor (ADR 0157,
# ADR 0147, ADR 0158). HOW BIG each release is, and its tag, is cut-tags.sh's
# question, asked per module too.
#
# Each module is measured on its own (lib/release-modules.sh): from the first
# parent of its own last tag to HEAD, over its own directory, the modules nested
# in that directory left out — so the root does not release because a family or
# a vendor changed, and a vendor that only had its tests touched does not
# release at all (#295). A module never tagged is measured from the empty tree:
# it is released when it holds anything that counts.
#
# Decision matrix (ADR 0007, ADR 0162, ADR 0170), per module:
#   a module other than the root: any path of it that counts      -> <dir> (rule 1m)
#   the root: a path under pkg/ or framework/ (outside a module),
#     the root go.mod, LICENSE or README.md, or a third-party/ or
#     framework/connectors/ path no module of go.work claims      -> .  (rule 1)
#   the root: a change under internal/ alone (while internal/ is
#     the root's)                                                  -> .  iff its bazel
#                                                                     rdeps reach //pkg/...,
#                                                                     //framework/... or
#                                                                     //third-party/... (rule 2)
#   nothing that counts                                            -> emit nothing (exit 0)
#
# Maintainer-only metadata — every *.md but README.md, every BUILD.bazel, and
# since ADR 0170 every *_test.go and testdata/ path, which no consumer's build
# compiles (#295, #220) — is dropped from each module's path list BEFORE any rule
# sees it, by the one predicate in lib/release-scope.sh (ADR 0089).
#
# Output: one module directory per line, `.` first, or nothing. Stable contract
# — consumed by cut-tags.sh and CI. (Before ADR 0170 it emitted the one token
# `sdk`, the root plus every vendor module that changed; cut-tags.sh still reads
# it, and `pkg`, `framework`, `third-party` and `vN` before it, that way.)

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/tag-format.sh
. "$here/lib/tag-format.sh"
# shellcheck source=lib/release-scope.sh
. "$here/lib/release-scope.sh"
# shellcheck source=lib/release-modules.sh
. "$here/lib/release-modules.sh"

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
compute-bumps.sh — print the directory of every module that needs a release.

Usage: $0 [--dry-run] [--explain] [--require-bazel] [--range=<rev>..HEAD]

Every module go.work names is measured from the first parent of its own last
tag (the root: the newest vX.Y.Z, or before the first one the newest
pkg/vX.Y.Z); a module never tagged from the empty tree. --range measures every
module over the one range given instead. Shallow-clone safe.

--explain writes, per module, the range, the rule that fired and the verdict to
stderr. stdout stays the stable contract (module directories, or nothing), so a
caller that parses it is unaffected.

--require-bazel refuses, instead of skipping rule 2, when bazel is absent and
the root's internal/ dirs changed. The release lane passes it; a developer
without bazel does not, and still gets every other rule.
EOF
      exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 64 ;;
  esac
done

# explain <line…> — the reason, on stderr, only when asked. #226 is that an
# internal/-only change publishes nothing and "the log cannot say why": every
# outcome below names itself, so a run page distinguishes "nothing to publish"
# from "the computation did not reach a verdict". stdout is untouched.
explain() {
  [ "$EXPLAIN" -eq 1 ] || return 0
  printf 'compute-bumps.sh: %s\n' "$@" >&2
}

# The modules, from go.work, refused rather than guessed (release_modules).
modules=""
modules="$(release_modules go.work)" || exit 1

# rdeps_reaches <layer-dir…> — rule 2: 0 when a changed internal/ layer reaches
# a published package through Bazel's reverse dependencies, 1 when none does
# (a MEASURED absence), and exit 1 — the whole script — when the question could
# not be asked. A changed file is reduced to its layer dir (internal/<layer>), a
# valid Bazel subtree //internal/<layer>/... rather than a bogus label built
# from a file name.
rdeps_reaches() {
  local modpath rdeps_out rdeps_rc
  if ! command -v bazel >/dev/null 2>&1; then
    # The missing-bazel guard is #227 defect 1 wearing a different hat: a
    # missing bazel and an rdeps set that reaches nothing produce the same empty
    # stdout and the same verdict, "no release". A developer without bazel still
    # gets every other rule, so the default stays a skip — announced — and the
    # release lane passes --require-bazel and gets a refusal.
    if [ "$REQUIRE_BAZEL" -eq 1 ]; then
      echo "compute-bumps.sh: bazel is not on PATH and --require-bazel was given" >&2
      echo "compute-bumps.sh: $# internal dir(s) would go unmeasured; refusing to report 'no release'" >&2
      exit 1
    fi
    explain "rule 2: bazel is not on PATH — $# internal dir(s) went UNMEASURED"
    return 1
  fi
  # stderr of the query, kept out of the caller's stream until we know whether
  # it describes a failure.
  rdeps_err="$(mktemp)"
  trap 'rm -f "$rdeps_err"' EXIT
  for modpath in "$@"; do
    [ -z "$modpath" ] && continue
    # A path that is not a Bazel subtree is not a failed query — it is a path
    # with nothing to ask about, and it must not turn into a red release. REAL,
    # not defensive: `internal/CLAUDE.md` reduces to itself, and the label
    # `//internal/CLAUDE.md/...` fails with exit 7 ("no targets found beneath").
    # The filter is structural — does any BUILD.bazel exist beneath the path —
    # rather than a match on that sentence, which is Bazel's to reword.
    if [ -z "$(find "$modpath" -name BUILD.bazel -print -quit 2>/dev/null)" ]; then
      explain "rule 2: //${modpath}/... is not a Bazel subtree (no BUILD.bazel beneath it) — skipped, not queried"
      continue
    fi
    # The ANSWER and the EXIT CODE, separately: a query that FAILED and one that
    # did not reach //pkg/... once produced the same silence and the same
    # verdict — how c707de5 (#214) landed with no tag cut (#227). A command
    # substitution reads to EOF, so no SIGPIPE can manufacture an exit code.
    rdeps_out=""
    rdeps_rc=0
    rdeps_out="$(bazel query "rdeps(//pkg/... + //framework/... + //third-party/..., //${modpath}/...)" 2>"$rdeps_err")" || rdeps_rc=$?
    if [ "$rdeps_rc" -ne 0 ]; then
      echo "compute-bumps.sh: bazel query failed (exit ${rdeps_rc}) for //${modpath}/..." >&2
      echo "compute-bumps.sh: refusing to report 'no release' for a computation that did not run" >&2
      cat "$rdeps_err" >&2
      exit 1
    fi
    # `case` on the variable, never `grep -c` (which prints 0 AND exits 1).
    case "$rdeps_out" in
      "") explain "rule 2: rdeps(published packages, //${modpath}/...) ran and reached nothing — a measured absence" ;;
      *)
        explain "rule 2: rdeps(published packages, //${modpath}/...) reaches a published package -> release"
        return 0
        ;;
    esac
  done
  return 1
}

# root_releases <counting-paths> — rule 1, then rule 2, over the root's paths
# that count. 0 when the root releases.
root_releases() {
  local counting="$1" path internal_raw
  local -a layers=()
  # Rule 1, through `while read` over the list rather than an unquoted array
  # expansion: under nullglob a path holding a glob character is matched
  # against the working tree, and a path matching nothing vanishes.
  while IFS= read -r path; do
    [ -z "$path" ] && continue
    case "$path" in
      third-party/* | framework/connectors/*/*)
        explain "rule 1: $path is a vendor-module path no module of go.work claims -> the SDK module releases"
        return 0
        ;;
      pkg/* | framework/* | go.mod | LICENSE | README.md)
        explain "rule 1: $path is an SDK-module change -> release"
        return 0
        ;;
    esac
  done <<<"$counting"
  # Rule 2: the internal/ layer dirs, reduced from the FILTERED list — after the
  # reduction a BENCH.md and a .go file are the same dir, so the exclusion has to
  # happen first (#237).
  internal_raw="$(awk -F/ '/^internal\//{ print $1 "/" $2 }' <<<"$counting" | LC_ALL=C sort -u)"
  if [ -z "$internal_raw" ]; then
    explain "rule 2: no internal/ dir in the filtered path list — nothing to query"
    return 1
  fi
  mapfile -t layers <<<"$internal_raw"
  rdeps_reaches "${layers[@]}"
}

release_list=""
total_changed=0
total_counting=0
while IFS= read -r dir; do
  [ -z "$dir" ] && continue
  range=""
  range="$(module_range "$dir" "$RANGE")" || exit 1
  # The changed paths, ONCE per module, and a git that failed is a refusal:
  # read as an empty list it is "no release", the shape #227 was opened for.
  changed_raw=""
  changed_rc=0
  changed_raw="$(module_paths "$dir" "$range" "$modules")" || changed_rc=$?
  if [ "$changed_rc" -ne 0 ]; then
    echo "compute-bumps.sh: git diff --name-only '$range' failed for $dir (exit ${changed_rc})" >&2
    echo "compute-bumps.sh: refusing to report 'no release' for a range that could not be read" >&2
    exit 1
  fi
  # Maintainer-only metadata dropped here and nowhere else, through a command
  # substitution whose status is CHECKED: a classifier that dies must not read
  # as "no path counts" (#226, #227, one layer further in). `printf '%s\n'`
  # because `while read` drops a final line without a newline.
  counting_raw=""
  counting_rc=0
  counting_raw="$(printf '%s\n' "$changed_raw" | release_scope_filter)" || counting_rc=$?
  if [ "$counting_rc" -ne 0 ]; then
    echo "compute-bumps.sh: the release-scope filter failed (exit ${counting_rc})" >&2
    echo "compute-bumps.sh: refusing to report 'no release' for a path list that could not be classified" >&2
    exit 1
  fi
  n_changed="$(awk 'NF { n++ } END { print n + 0 }' <<<"$changed_raw")"
  n_counting="$(awk 'NF { n++ } END { print n + 0 }' <<<"$counting_raw")"
  total_changed=$((total_changed + n_changed))
  total_counting=$((total_counting + n_counting))
  if is_new_range "$range"; then
    explain "module $dir: never tagged — measured from the empty tree"
  fi
  explain "module $dir: range: $range"
  explain "module $dir: changed paths: ${n_changed} (${n_counting} could carry a consumer-visible change)"
  [ "$n_counting" -gt 0 ] || continue
  if [ "$dir" = "." ]; then
    if root_releases "$counting_raw"; then
      release_list="${release_list}${dir}"$'\n'
    fi
  elif release_scope_any <<<"$counting_raw"; then
    explain "rule 1m: $(awk 'NF { print; exit }' <<<"$counting_raw") is a change of the module $dir -> release"
    release_list="${release_list}${dir}"$'\n'
  fi
done <<<"$modules"

explain "changed paths: ${total_changed} (${total_counting} could carry a consumer-visible change)"
if [ "$total_changed" -gt 0 ] && [ "$total_counting" -eq 0 ]; then
  explain "every changed path is maintainer-only metadata (*.md except README.md, BUILD.bazel, *_test.go, testdata/) or outside the published modules"
fi

# Emit the modules. Dry-run prints the same payload, no side effects.
if [ -z "$release_list" ]; then
  explain "verdict: NO RELEASE (nothing consumer-visible changed in any module)"
  exit 0
fi
explain "verdict: RELEASE (modules: $(awk 'NF' <<<"$release_list" | paste -sd' ' -))"
printf '%s' "$release_list"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "(dry-run: ${RANGE:-each module since its own last tag})" >&2
fi
