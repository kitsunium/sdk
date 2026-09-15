#!/usr/bin/env bash
# scripts/release/lib/release-scope.sh — the ONE notion of which repository paths
# can carry a consumer-visible change. Sourced by compute-bumps.sh (which decides
# WHETHER to cut a release) and cut-tags.sh (which decides HOW BIG). ADR 0089
# named that notion and put it in one function; this file is where it lives once,
# expressed as a CATEGORY instead of a list of names.
#
# Why it moved out of the two scripts. ADR 0089's `counts_for_release` and
# compute-bumps.sh's rule 1 each carried their own copy of the exclusion, both
# spelled `*/CLAUDE.md|*/BUILD.bazel`, under a comment describing a category:
# "maintainer-only metadata that ships in the module zip but carries no
# consumer-visible change — its churn alone must not cut a release". A `BENCH.md`
# is that category by the comment's own definition and was not in the list, so
# `pkg/v1/errs/BENCH.md` fell through to `pkg/v*/*` and cut `pkg/v0.4.4` from a
# 100 % documentary diff — ten files, zero lines of Go, an exported surface
# identical byte for byte to `pkg/v0.4.3` (issue #238). 78 `BENCH.md` are in the
# tree, 20 of them under `pkg/`, and each one was a trigger.
#
# So the fix is not a third name in a third place. A list of names reopens the
# issue for every new kind of maintainer file — #220 is the same defect on
# `*_test.go`, and ADR 0089's own §Deferred is the same defect on the
# `cut-tags.sh` side. The rule below answers the category question instead.
#
# THE RULE, and why this line and not another. A file in a module zip is
# consumer-visible when something a consumer uses renders or compiles it:
#
#   *.md      NO, with one exception. pkg.go.dev renders a package's README and
#             nothing else; every other Markdown file in the zip — CLAUDE.md,
#             BENCH.md, USES.md, CONVERGENCE.md — is surfaced nowhere a consumer
#             looks. README.md therefore keeps cutting releases and every other
#             .md stops. That exception is load-bearing, not timidity: README.md
#             under pkg/v1/ is generated FROM the code (ADR 0008) and is the page
#             a consumer reads on pkg.go.dev, and the docs portal copies
#             `pkg/<major>/**/README.md` out of the RELEASE TAG (ADR 0007 §5) —
#             so excluding it would mean a README fix could never reach either
#             surface until unrelated code cut a release.
#   BUILD.bazel NO. Build metadata for a build system the consumer does not run;
#             `go build` never reads it. Excluded since before ADR 0089.
#
# Nothing else is excluded, and in particular NOT `*_test.go` / `testdata/**`.
# That is issue #220 and it is deliberately still open here: a consumer does not
# run its dependencies' tests, so the argument is the same, but the scope call
# ("can an exported test helper be imported?") is a maintainer's to make, and
# ADR 0089 measured that widening it changes how real past releases were SIZED.
# When it is made, it is ONE line in rs_maintainer_only() and it reaches both
# halves at once. That is the whole point of this file.
#
# What the docs portal loses, stated rather than discovered later: `**/BENCH.md`
# is copied out of the release tag too, so a benchmark re-measurement now waits
# for the next release that a non-documentary change cuts. Delayed, and — with
# compute-bumps.sh --explain naming the reason — no longer silent. #227's reading
# applies: the next release's range still contains the commit.

# No JS counterpart in docs/site/scripts/lib/: that directory mirrors the tag
# SHAPE for the docs sync, which reads published GitHub releases and never the
# changed-path list. Nothing outside the release path asks this question.

set -euo pipefail

# The rule, as an awk prelude. One text, consumed in two modes below, because the
# two callers need different answers from the same question and a second copy of
# the question is what this file exists to prevent.
#
# awk and not bash `case`: both callers feed it a stream from `git`, and a reader
# that exits early makes git take SIGPIPE — `pipefail` then reports 141 and the
# caller reads a FALSE. This repository has lost a release to exactly that shape
# twice (ADR 0085, and the comment above compute-bumps.sh's rdeps query). awk
# consumes its input to EOF and answers through its exit status, so git always
# finishes writing. No `grep -q`, no `grep -c` (which prints 0 AND exits 1).
#
# Locals are declared as extra parameters — awk has no other scope, and a bare
# `n` inside a function is global, so two nested calls would clobber each other.
RELEASE_SCOPE_AWK='
function rs_basename(p,   n) { n = p; sub(/^.*\//, "", n); return n }

# 1 when <p> is maintainer-only metadata: in the zip, invisible to a consumer.
function rs_maintainer_only(p,   n) {
  n = rs_basename(p)
  if (n == "BUILD.bazel") return 1
  # Case-insensitive on both halves: a COVERAGE.MD must be excluded, and a
  # Readme.md must NOT be — misjudging the second under-counts, which is a
  # release that never gets cut, the worse of the two errors.
  if (n ~ /\.[Mm][Dd]$/ && toupper(n) != "README.MD") return 1
  return 0
}

# 1 when <p> could cut a release at all: inside a released module, and not
# maintainer-only. `pkg/` is the public module; `internal/` reaches it through
# the rdeps rule and is tagged in the same lockstep chain (ADR 0009).
function rs_releasable(p) {
  if (rs_maintainer_only(p)) return 0
  return (p ~ /^pkg\//) || (p ~ /^internal\//)
}
'

# release_scope_filter — stdin: paths, one per line. stdout: the ones that are
# NOT maintainer-only, unchanged and in order. The INCLUSION test stays with the
# caller (rule 1 wants `pkg/v*/*|pkg/go.mod`, rule 2 wants the module dir), so
# only the exclusion — the duplicated half — lives here.
release_scope_filter() {
  awk "$RELEASE_SCOPE_AWK"'
    $0 == ""                  { next }
    !rs_maintainer_only($0)   { print }
  '
}

# release_scope_any — stdin: paths, one per line. Exits 0 when at least one of
# them could cut a release. This is ADR 0089's `counts_for_release` question,
# asked of a path list instead of a sha so the sha lookup stays in cut-tags.sh.
release_scope_any() {
  awk "$RELEASE_SCOPE_AWK"'
    rs_releasable($0) { found = 1 }
    END               { exit !found }
  '
}
