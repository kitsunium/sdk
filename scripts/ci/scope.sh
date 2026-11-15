#!/usr/bin/env bash
# scripts/ci/scope.sh — what a run must test (ADR 0250): the whole suite, or
# what a pull request touches and everything that depends on it.
#
# A pull request runs the targets and packages its changes reach; a merge
# queue entry, a push to the default branch, the nightly run and a manual run
# run everything. Any doubt runs everything too: a path nobody can map, a
# change to the build's own definition, a computation that fails. The scope
# only ever narrows a run it can prove; it never decides a lane passes.
#
#   plan EVENT REF BASE EVAL [SELF_REGEX...]
#       The run's plan, as `key=value` lines for $GITHUB_OUTPUT:
#         mode=full|scoped|fallback   full: the event runs everything;
#                                     fallback: a scoped event that runs
#                                     everything, `reason` says why
#         reason=<one line>
#         touched=<import paths>      the Go packages the change reaches
#                                     directly (scoped only), space-separated
#         changed=<count>
#       BASE is the commit to compare with: the pull request's base; for a
#       push to another branch, empty, and the merge base with the default
#       branch (SCOPE_DEFAULT_BRANCH, main) is taken. A SELF_REGEX names the
#       files of the calling workflow — its own file, the scripts it reads —
#       whose change runs everything. The classification of every changed
#       path goes to SCOPE_REPORT (a file, appended) or stderr. Exit 0 with
#       an answer whatever happened; 64 on bad usage.
#
#   affected
#       Every package of the census whose package or test binary depends on a
#       package of SCOPE_TOUCHED, or is one, for the go environment of the
#       caller (GOOS, GOARCH, GOFLAGS' tags, GOEXPERIMENT): one line each,
#       `<module>\t<import path>\t<directory>`, the directory relative to the
#       repository. A Go program under a package's testdata/ — built by that
#       package's tests — counts as part of it. Exit 1 when go list fails.
#
#   lane NAME [CONFIG...] [-- IMPORTPATH...]
#       `NAME=run` or `NAME=skip`: run unless SCOPE_MODE is scoped and no
#       package `affected` reaches under any CONFIG — `GOOS=x,GOARCH=y` style
#       assignments, `host` for the caller's environment — is one of the
#       import paths given (any package, when none is given). A failed
#       computation answers run.
#
#   bazel OUTDIR
#       The Bazel job's scope: SCOPE_RESULT (the scope job's result),
#       SCOPE_MODE, SCOPE_REASON and SCOPE_BASE from the scope job; when the
#       plan is scoped, target-determinator (TD_VERSION, its linux/amd64
#       binary's TD_SHA256) between SCOPE_BASE and the checkout writes
#       OUTDIR/targets.txt (every affected target but the manual ones),
#       OUTDIR/tests.txt (the tests among them) and OUTDIR/alloc.txt (the
#       tests of tools/alloc-lane-targets.txt among them) — run in a clone of
#       the checkout, never in it. Prints mode, reason and the three counts;
#       mode=fallback whenever anything fails. Exit 2 — the one failure no
#       fallback answers — when the checkout under test changed meanwhile.
#
#   pkgs FILE MODULE [PREFIX]
#       The import paths of FILE (an `affected` answer) in MODULE, under
#       PREFIX when given, one per line.
#
#   go-test FILE PREFIX [FLAG...]
#       `go test FLAG... <packages>` in each module of FILE, GOWORK=off, for
#       the packages under PREFIX (every package for an empty PREFIX), a
#       hundred at a time; every module runs, exit 1 when one failed.
#
#   keep FILE DIR REL...
#       The RELs (package patterns relative to DIR, `./fs/flock`) whose
#       directory is in FILE.
#
#   summary
#       The lanes of a workflow against the plan, as Markdown on stdout:
#       SCOPE_RESULT, SCOPE_MODE, SCOPE_REASON, SCOPE_TOUCHED, and
#       SCOPE_LANES, one `name|decision|result|note` line a lane — decision
#       `run` or `skip` as the scope job decided it, `full` for a lane never
#       scoped, `result` the job's (success, failure, cancelled, skipped).
#       Exit 1 when a lane that had to run was skipped: a plan that failed, a
#       full run, a fallback, a lane never scoped and a lane in scope all have
#       to run.
#
#   rules
#       The path tables below, for the suite.

set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
self="$root/scripts/ci/scope.sh"

# One scratch directory a process, removed on exit: each subcommand runs once.
tmp=""
scratch() {
  if [ -z "$tmp" ]; then
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
  fi
}

usage() {
  sed -n '2,/^set -euo/p' "$self" | sed '$d' | sed 's/^# \{0,1\}//' >&2
  exit 64
}

# fallback_rules — the changes that run everything, whatever else changed: the
# build's own definition (Bazel's module, flags, version, the root package,
# what //... holds), any module's requirements, the workspace, tools/ (the
# allocation lane's list, the stamping script, the docs/api writer), how git
# checks the tree out, and this script. Tab-separated: the regular expression
# over a path from the repository root, then what it is.
fallback_rules() {
  cat <<'EOF'
^MODULE\.bazel(\.lock)?$	the Bazel module
^\.bazelrc$	Bazel's flags
^\.bazelversion$	Bazel's version
^\.bazelignore$	what //... holds
^BUILD\.bazel$	the root package
^WORKSPACE	a WORKSPACE file
(^|/)go\.(mod|sum)$	a module's requirements
^go\.work(\.sum)?$	the workspace
^tools/	the repository's tools
^\.gitattributes$	how git checks the tree out
^scripts/ci/scope\.sh$	the scope itself
EOF
}

# reader_rules — files a test reads outside its own package directory, and
# the packages whose tests read them. Bazel declares each as `data` (a test in
# its sandbox reads nothing else); `go test` reads them through the checkout,
# and no import says so. scope.bats holds this table to every cross-package
# `data` label of the BUILD files. Tab-separated: the expression, then the
# readers' directories.
#
#   - any Go source: the error-code audits of //:audit_sources, and
#     tools/genindex's census of the exported API;
#   - docs/api and docs/error-codes.yaml, the census's other side; the
#     platforms table, which genindex reads;
#   - design/net.yaml and pkg/v1/net/server's facade, read by
#     internal/service/net/server's registry and option tests;
#   - framework/internal/app's sources and catalogue, and the common-password
#     list, read by framework/app's no-trace guard; the consensus core's
#     sources, read by the engine's conformance test;
#   - the sqlite connector's former-refs fixture, read by the other two.
reader_rules() {
  cat <<'EOF'
\.go$	internal/kernel/errs internal/core/app/view internal/core/data/codec internal/service/app/cli internal/service/app/health internal/service/app/lifecycle internal/service/app/lock internal/service/app/scheduler internal/service/app/view internal/service/data/sql tools/genindex
^docs/api/	tools/genindex
^docs/error-codes\.yaml$	tools/genindex
^scripts/ci/platforms\.sh$	tools/genindex
^design/net\.yaml$	internal/service/net/server
^pkg/v1/net/server/gen_facade\.go$	internal/service/net/server
^framework/internal/app/	framework/app
^internal/core/app/consensus/[^/]*\.go$	internal/service/app/consensus
^internal/service/crypto/password/commonpw/	framework/app
^framework/connectors/sqlite/testdata/	framework/connectors/mysql framework/connectors/postgres
EOF
}

# go_inert_rules — files no Go test reads even inside a package directory:
# Bazel's own. Bazel's lane judges them through target-determinator.
go_inert_rules() {
  cat <<'EOF'
(^|/)BUILD\.bazel$
\.bzl$
EOF
}

# inert_rules — paths outside every Go package that no Go test reads: prose,
# the design (kit's input, held to the code by `make api-check` in every run),
# the docs portal and the ADRs, the workflows and the shell tooling (their
# suites run in full in every run), the C reference benchmarks, the linters'
# and reviewers' configuration. Anything outside a package and outside this
# list runs everything.
inert_rules() {
  cat <<'EOF'
\.md$
^LICENSE$
^docs/
^design/
^\.github/
^scripts/
^bench/
(^|/)\.gitignore$
^\.golangci\.yml$
^\.ktn-linter\.yaml$
^\.coderabbit(\.local)?\.yaml$
^\.shellcheckrc$
^Makefile$
EOF
}

say() { printf '%s\n' "$*"; }
report() {
  if [ -n "${SCOPE_REPORT:-}" ]; then
    printf '%s\n' "$*" >>"$SCOPE_REPORT"
  else
    printf '%s\n' "$*" >&2
  fi
}

# answer MODE REASON [TOUCHED] [CHANGED] [TESTS] — the plan's lines.
answer() {
  say "mode=$1"
  say "reason=$2"
  say "touched=${3:-}"
  say "tests=${5:-}"
  say "changed=${4:-0}"
  report "scope: $1 — $2"
}

# ── plan ────────────────────────────────────────────────────────────────────

plan() {
  [ "$#" -ge 4 ] || usage
  local event="$1" ref="$2" base="$3" eval_sha="$4"
  shift 4
  local default="${SCOPE_DEFAULT_BRANCH:-main}"

  case "$event" in
    pull_request) ;;
    push)
      case "$ref" in
        "refs/heads/$default") answer full "a push to $default runs everything"; return 0 ;;
        refs/heads/gh-readonly-queue/*) answer full "a merge queue branch runs everything"; return 0 ;;
        refs/heads/*) ;;
        *) answer full "a push of $ref runs everything"; return 0 ;;
      esac
      ;;
    merge_group) answer full "a merge queue entry runs everything"; return 0 ;;
    schedule) answer full "the nightly run runs everything"; return 0 ;;
    workflow_dispatch) answer full "a manual run runs everything"; return 0 ;;
    *) answer full "the event '$event' is not scoped"; return 0 ;;
  esac

  if [ -z "$eval_sha" ]; then
    answer fallback "no commit to evaluate"
    return 0
  fi
  if [ -z "$base" ] && [ "$event" = push ]; then
    base="$(git -C "$root" merge-base "origin/$default" "$eval_sha" 2>/dev/null)" || base=""
  fi
  if [ -z "$base" ]; then
    answer fallback "no base commit to compare with"
    return 0
  fi
  local c
  for c in "$base" "$eval_sha"; do
    if ! git -C "$root" rev-parse --verify --quiet "$c^{commit}" >/dev/null; then
      answer fallback "commit $c is not in this clone (fetch-depth 0 is required)"
      return 0
    fi
  done

  scratch

  # --no-renames: a rename lists its source too, so a file moved out of a
  # package still counts as a change to it.
  if ! git -C "$root" diff --no-renames --name-only "$base" "$eval_sha" >"$tmp/changed"; then
    answer fallback "git diff $base $eval_sha failed"
    return 0
  fi
  local changed
  changed="$(awk 'END { print NR }' "$tmp/changed")"
  report "scope: $changed path(s) changed between $base and $eval_sha"

  # The calling workflow's own files, then the rules every lane shares.
  : >"$tmp/fallback"
  local re
  for re in "$@"; do
    printf '%s\tthe workflow'"'"'s own files\n' "$re" >>"$tmp/fallback"
  done
  fallback_rules >>"$tmp/fallback"
  reader_rules >"$tmp/readers"
  go_inert_rules >"$tmp/goinert"
  inert_rules >"$tmp/inert"

  # Directories holding Go files at either commit — a package deleted by the
  # change is one it touches — outside testdata/ and the directories the go
  # command ignores (a leading . or _).
  if ! { git -C "$root" ls-tree -r --name-only "$base" && git -C "$root" ls-tree -r --name-only "$eval_sha"; } >"$tmp/tree"; then
    answer fallback "git ls-tree failed"
    return 0
  fi
  awk '
    /\.go$/ {
      d = $0
      if (sub(/\/[^\/]*$/, "", d) == 0) d = "."
      n = split(d, c, "/")
      for (i = 1; i <= n; i++) if (c[i] == "testdata" || c[i] ~ /^[._]/) next
      if (!(d in seen)) { seen[d] = 1; print d }
    }
  ' "$tmp/tree" >"$tmp/pkgdirs"

  # The packages that embed files (//go:embed in a non-test file): a file
  # beside their sources may be compiled into them.
  : >"$tmp/embeds"
  for c in "$base" "$eval_sha"; do
    git -C "$root" grep -l -E '^[[:space:]]*//go:embed' "$c" -- '*.go' ':!*_test.go' 2>/dev/null |
      sed -e "s#^$c:##" -e 's#/[^/]*$##' >>"$tmp/embeds" || true
  done

  # The modules at the evaluated commit, with their paths: the census.
  : >"$tmp/modules"
  local mod modpath
  while IFS= read -r mod; do
    modpath="$(git -C "$root" show "$eval_sha:${mod#./}go.mod" 2>/dev/null | awk '$1 == "module" { print $2; exit }')" || modpath=""
    if [ -z "$modpath" ]; then
      answer fallback "the module at '$mod' names no module path"
      return 0
    fi
    printf '%s\t%s\n' "${mod%/}" "$modpath" >>"$tmp/modules"
  done < <(git -C "$root" ls-tree -r --name-only "$eval_sha" | awk '
    $0 == "go.mod" { print "./"; next }
    /\/go\.mod$/ {
      d = $0; sub(/go\.mod$/, "", d)
      n = split(d, c, "/")
      for (i = 1; i <= n; i++) if (c[i] == "testdata") next
      print d
    }')
  if [ ! -s "$tmp/modules" ]; then
    answer fallback "no module at $eval_sha"
    return 0
  fi

  # Each changed path, classified: what makes everything run, what it
  # touches (its package, the packages that read it), or nothing.
  awk -F'\t' '
    FILENAME == ARGV[1] { fb[++nfb] = $1; fbw[nfb] = $2; next }
    FILENAME == ARGV[2] { rd[++nrd] = $1; rdw[nrd] = $2; next }
    FILENAME == ARGV[3] { gi[++ngi] = $1; next }
    FILENAME == ARGV[4] { inr[++nin] = $1; next }
    FILENAME == ARGV[5] { pkg[$1] = 1; next }
    FILENAME == ARGV[6] { md[++nmd] = $1; mp[$1] = $2; next }
    FILENAME == ARGV[7] { emb[$1] = 1; next }
    function module_of(p,    i, best, m) {
      best = "."
      for (i = 1; i <= nmd; i++) {
        m = md[i]
        if (m == ".") continue
        if (index(p, m "/") == 1 && length(m) > length(best == "." ? "" : best)) best = m
      }
      return best
    }
    function importpath(d,    m) {
      m = module_of(d "/x")
      if (!(m in mp)) return ""
      if (d == m) return mp[m]
      if (m == ".") return (d == "." ? mp[m] : mp[m] "/" d)
      return mp[m] "/" substr(d, length(m) + 2)
    }
    function touch(kind, d, why,    ip) {
      ip = importpath(d)
      if (ip == "") { print "unowned\t" why "\t" d " is in no module"; return }
      print kind "\t" ip "\t" why
    }
    {
      p = $0
      for (i = 1; i <= nfb; i++) if (p ~ fb[i]) { print "fallback\t" p "\t" fbw[i]; next }
      accounted = 0
      for (i = 1; i <= nrd; i++) if (p ~ rd[i]) {
        n = split(rdw[i], r, " ")
        for (j = 1; j <= n; j++) touch("own", r[j], p " is read by the tests of " r[j])
        accounted = 1
      }
      for (i = 1; i <= ngi; i++) if (p ~ gi[i]) { print "inert\t" p "\tread by no Go test"; next }
      m = module_of(p)
      d = p
      if (sub(/\/[^\/]*$/, "", d) == 0) d = "."
      found = ""
      while (1) {
        if (d in pkg) { found = d; break }
        if (d == m || d == ".") break
        if (sub(/\/[^\/]*$/, "", d) == 0) d = "."
      }
      if (found != "") {
        # What the package compiles: a non-test Go file of its own directory,
        # a native source beside it (assembly, C, C++, Objective-C, a .syso
        # object: the go command links them into the package) or any file
        # beside the sources of a package that embeds files. A
        # test file, a testdata/ file or a file nothing compiles reaches the
        # package'"'"'s own tests alone.
        dir = p
        if (sub(/\/[^\/]*$/, "", dir) == 0) dir = "."
        if (p ~ /\.go$/) kind = (p !~ /_test\.go$/ && dir == found) ? "touch" : "own"
        else kind = (((found in emb) || p ~ /\.(s|S|c|h|cc|cpp|cxx|hh|hpp|hxx|m|syso)$/) && ("/" p) !~ /\/testdata\//) ? "touch" : "own"
        touch(kind, found, p)
        next
      }
      if (accounted) next
      for (i = 1; i <= nin; i++) if (p ~ inr[i]) { print "inert\t" p "\toutside every Go package, read by no Go test"; next }
      print "unowned\t" p "\toutside every Go package, and no rule says what reads it"
    }
  ' "$tmp/fallback" "$tmp/readers" "$tmp/goinert" "$tmp/inert" "$tmp/pkgdirs" "$tmp/modules" "$tmp/embeds" "$tmp/changed" >"$tmp/classes"

  local first
  first="$(awk -F'\t' '$1 == "fallback" { print $2 " changed (" $3 ")"; exit }' "$tmp/classes")"
  if [ -n "$first" ]; then
    report_classes "$tmp/classes"
    answer fallback "$first" "" "$changed"
    return 0
  fi
  first="$(awk -F'\t' '$1 == "unowned" { print $2 ": " $3; exit }' "$tmp/classes")"
  if [ -n "$first" ]; then
    report_classes "$tmp/classes"
    answer fallback "$first" "" "$changed"
    return 0
  fi

  local touched tests
  touched="$(awk -F'\t' '$1 == "touch" { print $2 }' "$tmp/classes" | LC_ALL=C sort -u | tr '\n' ' ' | sed 's/ $//')"
  # A package whose code changed runs its tests anyway: listed once.
  tests="$(awk -F'\t' '$1 == "touch" { t[$2] = 1 } $1 == "own" { o[$2] = 1 } END { for (p in o) if (!(p in t)) print p }' "$tmp/classes" | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//')"
  report_classes "$tmp/classes"
  # An output is read back into one environment variable a step receives;
  # past this size the list stops being worth passing, and the run is whole.
  if [ "$((${#touched} + ${#tests}))" -gt "${SCOPE_TOUCHED_MAX:-60000}" ]; then
    answer fallback "the change touches too many packages to list" "" "$changed"
    return 0
  fi
  local n m
  n="$(wc -w <<<"$touched" | tr -d ' ')"
  m="$(wc -w <<<"$tests" | tr -d ' ')"
  answer scoped "$changed path(s) changed: $n Go package(s) whose code changed, $m more whose tests alone did" "$touched" "$changed" "$tests"
}

# report_classes FILE — every changed path and what it is, at most 200.
report_classes() {
  awk -F'\t' '
    NR <= 200 {
      if ($1 == "touch") print "  touches " $2 "  (" $3 ")"
      else if ($1 == "own") print "  runs the tests of " $2 "  (" $3 ")"
      else if ($1 == "fallback") print "  runs everything: " $2 "  (" $3 ")"
      else if ($1 == "unowned") print "  runs everything: " $2 "  (" $3 ")"
      else print "  ignored: " $2 "  (" $3 ")"
    }
    END { if (NR > 200) print "  … and " NR - 200 " more" }
  ' "$1" | while IFS= read -r line; do report "$line"; done
}

# ── affected ────────────────────────────────────────────────────────────────

# testdata_programs MODULE — the directories under a testdata/ of MODULE
# holding Go files, outside nested modules and fixture modules, one per line,
# relative to the repository.
testdata_programs() {
  local mod="$1"
  (cd "$root" && git ls-files -- "$mod") | awk -v mod="$mod" '
    /\/testdata\// && /\.go$/ { d = $0; sub(/\/[^\/]*$/, "", d); progs[d] = 1 }
    $0 == "go.mod" { mods["."] = 1 }
    /\/go\.mod$/ { d = $0; sub(/\/go\.mod$/, "", d); mods[d] = 1 }
    END {
      for (p in progs) {
        # Inside a nested module, or under a go.mod of its own: not this
        # module'"'"'s program.
        ok = 1
        for (m in mods) {
          if (m == mod) continue
          if (m == "." ) continue
          if (index(p "/", m "/") == 1) { ok = 0; break }
        }
        if (ok) print p
      }
    }' | LC_ALL=C sort
}

# unsound — reads `go list -e` lines (import path, error, `incomplete` or
# nothing; tab-separated) and prints the first package that keeps the listing
# from proving what reaches what: one that has an error, or whose own graph
# lacks a package. `-e` lets the command succeed over those, so a dependent
# could be missing from the answer and its tests skipped. The one error that
# proves nothing wrong is a package no file of builds on this platform: it is
# left out of the run, as a platform's constraints decide.
unsound() {
  awk -F'\t' '
    $2 !~ /build constraints exclude all Go files/ && ($2 != "" || $3 != "") {
      print $1 ": " ($2 != "" ? $2 : "its dependency graph is incomplete")
      exit
    }'
}

affected() {
  local touched="${SCOPE_TOUCHED-}" tests="${SCOPE_TESTS-}"
  scratch
  # shellcheck disable=SC2086 # one import path per word
  printf '%s\n' $touched >"$tmp/touched"
  # shellcheck disable=SC2086 # one import path per word
  printf '%s\n' $tests >"$tmp/tests"

  local modules
  if ! modules="$(bash "$root/scripts/ci/go-modules.sh")"; then
    echo "scope: the module census failed" >&2
    return 1
  fi
  : >"$tmp/list"
  local mod modpath prog owner rel deps bad
  for mod in $modules; do
    modpath="$(awk '$1 == "module" { print $2; exit }' "$root/$mod/go.mod")"
    if [ -z "$modpath" ]; then
      echo "scope: $mod/go.mod names no module" >&2
      return 1
    fi
    # One line a package and a test variant: its import path, its error
    # (quoted: a message is one line), `incomplete` when it or a package it
    # links has one, and every package it links. The test binary's line
    # (`<p>.test`) links what the tests do.
    if ! (cd "$root/$mod" && GOWORK=off go list -e -test \
      -f '{{.ImportPath}}{{"\t"}}{{if .Error}}{{printf "%q" .Error.Err}}{{end}}{{"\t"}}{{if or .Incomplete .DepsErrors}}incomplete{{end}}{{"\t"}}{{join .Deps ","}}' ./...) >"$tmp/l"; then
      echo "scope: go list failed in $mod" >&2
      return 1
    fi
    # A listing that cannot prove its graph narrows nothing: the caller runs
    # everything.
    if bad="$(unsound <"$tmp/l")" && [ -n "$bad" ]; then
      echo "scope: go list in $mod is not sound ($bad)" >&2
      return 1
    fi
    awk -F'\t' -v mod="$mod" -v mp="$modpath" '{ print mod "\t" mp "\t" $1 "\t" $2 "\t" $4 }' "$tmp/l" >>"$tmp/list"
    # A program a package's tests build from its testdata/ links what it
    # imports: those are the package's dependencies too.
    while IFS= read -r prog; do
      [ -n "$prog" ] || continue
      owner="${prog%%/testdata/*}"
      if [ "$mod" = . ]; then rel="$prog"; else rel="${prog#"$mod"/}"; fi
      if ! deps="$(cd "$root/$mod" && GOWORK=off go list -e -deps \
        -f '{{.ImportPath}}{{"\t"}}{{if .Error}}{{printf "%q" .Error.Err}}{{end}}{{"\t"}}{{if or .Incomplete .DepsErrors}}incomplete{{end}}' "./$rel")"; then
        echo "scope: go list failed for $prog" >&2
        return 1
      fi
      if bad="$(unsound <<<"$deps")" && [ -n "$bad" ]; then
        echo "scope: go list for $prog is not sound ($bad)" >&2
        return 1
      fi
      deps="$(awk -F'\t' '{ printf "%s,", $1 }' <<<"$deps")"
      if [ "$mod" = . ]; then rel="$owner"; else rel="${owner#"$mod"}"; rel="${rel#/}"; fi
      printf '%s\t%s\t%s\t\t%s\n' "$mod" "$modpath" "$modpath${rel:+/$rel}" "$deps" >>"$tmp/list"
    done < <(testdata_programs "$mod")
  done

  awk -F'\t' '
    FILENAME == ARGV[1] { if ($1 != "") t[$1] = 1; next }
    FILENAME == ARGV[2] { if ($1 != "") own[$1] = 1; next }
    {
      mod = $1; mp = $2; ip = $3; err = $4; deps = $5
      owner = ip
      sub(/ \[.*\]$/, "", owner)
      if (owner ~ /\.test$/) sub(/\.test$/, "", owner)
      else if (owner ~ /_test$/ && ip ~ / \[/) sub(/_test$/, "", owner)
      if (ip == owner && !(owner in seen)) {
        seen[owner] = 1
        # A package no file of builds here is not one this platform tests.
        if (err ~ /build constraints exclude all Go files/) excluded[owner] = 1
        order[++n] = owner; modof[owner] = mod; mpof[owner] = mp
      }
      if (owner in hit) next
      if ((owner in t) || (owner in own)) { hit[owner] = 1; next }
      m = split(deps, d, ",")
      for (i = 1; i <= m; i++) {
        x = d[i]
        sub(/ \[.*\]$/, "", x)
        if (x in t) { hit[owner] = 1; next }
      }
    }
    END {
      for (i = 1; i <= n; i++) {
        o = order[i]
        if (!(o in hit) || (o in excluded)) continue
        mod = modof[o]; mp = mpof[o]
        if (o == mp) dir = mod
        else if (index(o, mp "/") == 1) dir = (mod == "." ? "" : mod "/") substr(o, length(mp) + 2)
        else continue
        print mod "\t" o "\t" dir
      }
    }
  ' "$tmp/touched" "$tmp/tests" "$tmp/list"
}

# ── lane ────────────────────────────────────────────────────────────────────

lane() {
  [ "$#" -ge 1 ] || usage
  local name="$1"
  shift
  local configs=() filters=() seen_sep=0 a
  for a in "$@"; do
    if [ "$seen_sep" -eq 1 ]; then filters+=("$a"); continue; fi
    if [ "$a" = -- ]; then seen_sep=1; continue; fi
    configs+=("$a")
  done
  [ "${#configs[@]}" -gt 0 ] || configs=(host)

  if [ "${SCOPE_MODE:-}" != scoped ]; then
    say "$name=run"
    echo "scope: $name runs: the plan is '${SCOPE_MODE:-none}'" >&2
    return 0
  fi
  if [ -z "${SCOPE_TOUCHED:-}" ] && [ -z "${SCOPE_TESTS:-}" ]; then
    say "$name=skip"
    echo "scope: $name skipped: no Go package touched" >&2
    return 0
  fi

  local cfg out reached="" envs
  scratch
  out="$tmp/affected"
  for cfg in "${configs[@]}"; do
    envs=()
    if [ "$cfg" != host ]; then
      IFS=, read -r -a envs <<<"$cfg"
    fi
    if ! env ${envs[@]+"${envs[@]}"} bash "$self" affected >"$out"; then
      say "$name=run"
      echo "::warning::scope: the packages $name reaches could not be computed for '$cfg': it runs" >&2
      return 0
    fi
    if [ "${#filters[@]}" -gt 0 ]; then
      reached="$reached$(awk -F'\t' 'FNR == NR { f[$1] = 1; next } ($2 in f) { print $2 }' <(printf '%s\n' "${filters[@]}") "$out")"
    else
      reached="$reached$(cut -f2 "$out")"
    fi
  done
  if [ -n "$reached" ]; then
    say "$name=run"
    echo "scope: $name runs: $(printf '%s\n' "$reached" | LC_ALL=C sort -u | awk 'NR <= 20' | tr '\n' ' ')" >&2
  else
    say "$name=skip"
    echo "scope: $name skipped: none of ${SCOPE_TOUCHED} ${SCOPE_TESTS:-} reaches it" >&2
  fi
}

# ── bazel ───────────────────────────────────────────────────────────────────

# The universe target-determinator compares: every target but the manual
# ones, which `bazel test //...` leaves out too — named one by one, a manual
# test would run.
TD_TARGETS='//... except attr(tags, "manual", //...)'

# td_install DIR — the pinned target-determinator, verified, its path printed.
td_install() {
  local dir="$1" bin url
  if [ -n "${SCOPE_TD:-}" ]; then
    # The suite's stub. Never set by a workflow.
    echo "$SCOPE_TD"
    return 0
  fi
  if [ -z "${TD_VERSION:-}" ] || [ -z "${TD_SHA256:-}" ]; then
    echo "scope: TD_VERSION and TD_SHA256 pin target-determinator; one is missing" >&2
    return 1
  fi
  mkdir -p "$dir"
  bin="$dir/target-determinator"
  url="https://github.com/bazel-contrib/target-determinator/releases/download/${TD_VERSION}/target-determinator.linux.amd64"
  if ! curl -fsSL --retry 3 -o "$bin.part" "$url"; then
    echo "scope: could not download $url" >&2
    return 1
  fi
  if ! printf '%s  %s\n' "$TD_SHA256" "$bin.part" | sha256sum -c - >/dev/null 2>&1; then
    echo "scope: target-determinator $TD_VERSION does not match its pinned SHA-256" >&2
    rm -f "$bin.part"
    return 1
  fi
  mv "$bin.part" "$bin"
  chmod +x "$bin"
  echo "$bin"
}

# bazel_query OUT EXPR — `bazel query EXPR`, labels only, into OUT.
bazel_query() {
  local out="$1" q
  q="$(mktemp)"
  printf '%s\n' "$2" >"$q"
  if ! (cd "$root" && "${BAZEL:-bazel}" query --query_file="$q" --output=label 2>"$out.log") | LC_ALL=C sort -u >"$out"; then
    rm -f "$q"
    return 1
  fi
  rm -f "$q"
}

bazel_scope() {
  [ "$#" -eq 1 ] || usage
  local out="$1"
  mkdir -p "$out"
  : >"$out/targets.txt"
  : >"$out/tests.txt"
  : >"$out/alloc.txt"
  emit() {
    say "mode=$1"
    say "reason=$2"
    say "targets=$(awk 'END { print NR }' "$out/targets.txt")"
    say "tests=$(awk 'END { print NR }' "$out/tests.txt")"
    say "alloc=$(awk 'END { print NR }' "$out/alloc.txt")"
    echo "scope: bazel $1 — $2" >&2
  }

  if [ "${SCOPE_RESULT:-}" != success ]; then
    emit fallback "the scope job ended '${SCOPE_RESULT:-unknown}': everything runs"
    return 0
  fi
  case "${SCOPE_MODE:-}" in
    scoped) ;;
    full)
      emit full "${SCOPE_REASON:-the plan is full}"
      return 0
      ;;
    *)
      emit fallback "${SCOPE_REASON:-the scope job gave no plan}"
      return 0
      ;;
  esac
  if [ -z "${SCOPE_BASE:-}" ]; then
    emit fallback "no base commit for target-determinator"
    return 0
  fi

  local td
  if ! td="$(td_install "$out/bin")"; then
    emit fallback "target-determinator could not be installed"
    return 0
  fi

  # target-determinator checks the base out where it runs and the original
  # commit back when it is done — unless it is killed, or that checkout
  # fails, and a build of the base would then pass for the pull request's.
  # So it never runs in the checkout under test: in a clone of it sharing
  # its objects, removed with the Bazel output base it made.
  local head status clone ok=1
  if ! head="$(git -C "$root" rev-parse HEAD)" || ! status="$(git -C "$root" status --porcelain --untracked-files=no)"; then
    emit fallback "git cannot name the commit under test"
    return 0
  fi
  clone="$out/clone"
  rm -rf "$clone"
  # No hook of the host's runs here: a post-checkout hook that rejects would
  # fail the clone and the plan fall back for a reason that is not the change.
  if ! git -c core.hooksPath=/dev/null clone --quiet --shared --no-checkout "$root" "$clone" 2>"$out/clone.log" ||
    ! git -C "$clone" -c core.hooksPath=/dev/null checkout --quiet --detach "$head" 2>>"$out/clone.log"; then
    rm -rf "$clone"
    emit fallback "the commit under test could not be cloned for target-determinator"
    return 0
  fi
  (cd "$clone" && "$td" -bazel="${BAZEL:-bazel}" -bazel-opts=--config=ci -targets="$TD_TARGETS" \
    -delete-cached-worktree "$SCOPE_BASE") >"$out/td.out" 2>"$out/td.log" || ok=0
  # Its server and its output base go with it: the build needs both back.
  (cd "$clone" && "${BAZEL:-bazel}" clean --expunge) >/dev/null 2>&1 || true
  rm -rf "$clone"
  # The checkout under test is what it was, or nothing this job runs can be
  # trusted — and no fallback answers that.
  if [ "$(git -C "$root" rev-parse HEAD)" != "$head" ] ||
    [ "$(git -C "$root" status --porcelain --untracked-files=no)" != "$status" ]; then
    echo "scope: the checkout under test changed while target-determinator ran" >&2
    exit 2
  fi
  if [ "$ok" -ne 1 ]; then
    tail -n 40 "$out/td.log" >&2 || true
    emit fallback "target-determinator failed"
    return 0
  fi
  # Labels only: anything else on its standard output is not a target.
  awk '/^\/\// { print }' "$out/td.out" | LC_ALL=C sort -u >"$out/targets.txt"

  if [ -s "$out/targets.txt" ]; then
    local set
    set="$(awk '{ printf "%s\"%s\"", (NR > 1 ? " " : ""), $0 }' "$out/targets.txt")"
    if ! bazel_query "$out/tests.txt" "tests(set($set))"; then
      : >"$out/tests.txt"
      emit fallback "bazel query could not list the affected tests"
      return 0
    fi
    local alloc
    alloc="$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$root/tools/alloc-lane-targets.txt" | tr -s '[:space:]' ' ' | sed 's/ $//')"
    if [ -z "${alloc// /}" ]; then
      emit fallback "tools/alloc-lane-targets.txt names no target"
      return 0
    fi
    if ! bazel_query "$out/alloc.txt" "tests(set($alloc) intersect set($set))"; then
      : >"$out/alloc.txt"
      emit fallback "bazel query could not list the affected allocation tests"
      return 0
    fi
  fi
  emit scoped "target-determinator against $SCOPE_BASE"
}

# ── pkgs, go-test, keep ─────────────────────────────────────────────────────

pkgs() {
  [ "$#" -ge 2 ] || usage
  # An empty prefix is every package: index(s, "") is 0 in some awks.
  awk -F'\t' -v mod="$2" -v pre="${3:-}" '$1 == mod && (pre == "" || index($2, pre) == 1) { print $2 }' "$1"
}

go_test() {
  [ "$#" -ge 2 ] || usage
  local file="$1" prefix="$2"
  shift 2
  local fail=0 any=0 mod mods list
  # A scope file that cannot be read is not an empty selection: an empty one
  # runs nothing and passes, an unreadable one has judged nothing and fails.
  if [ ! -r "$file" ] || [ -d "$file" ]; then
    echo "go-test: cannot read the scope file '$file'" >&2
    return 1
  fi
  if ! mods="$(awk -F'\t' '!($1 in s) { s[$1] = 1; print $1 }' "$file")"; then
    echo "go-test: cannot read the scope file '$file'" >&2
    return 1
  fi
  while IFS= read -r mod; do
    [ -n "$mod" ] || continue
    if ! list="$(pkgs "$file" "$mod" "$prefix")"; then
      echo "go-test: cannot read the scope file '$file'" >&2
      return 1
    fi
    [ -n "$list" ] || continue
    any=1
    echo "::group::$mod ($(wc -l <<<"$list" | tr -d ' ') package(s) in scope)"
    # A hundred at a time: a command line holds that many import paths on
    # every runner, Windows' 32 KiB included.
    if ! (cd "$root/$mod" && printf '%s\n' "$list" | GOWORK=off xargs -n 100 go test "$@"); then
      echo "::error::$mod has failing tests among the packages in scope"
      fail=1
    fi
    echo "::endgroup::"
  done <<<"$mods"
  if [ "$any" -eq 0 ]; then
    echo "go-test: no package${prefix:+ under $prefix} in scope"
  fi
  return "$fail"
}

keep() {
  [ "$#" -ge 2 ] || usage
  local file="$1" dir="$2"
  shift 2
  local rel d
  for rel in "$@"; do
    d="$dir/${rel#./}"
    d="${d%/}"
    d="${d%/.}"
    if awk -F'\t' -v d="$d" '$3 == d { found = 1; exit } END { exit !found }' "$file"; then
      printf '%s\n' "$rel"
    fi
  done
}

# ── summary ─────────────────────────────────────────────────────────────────

summary() {
  local result="${SCOPE_RESULT:-}" mode="${SCOPE_MODE:-}" reason="${SCOPE_REASON:-}"
  local why state fail=0 must
  if [ "$result" != success ]; then
    mode=fallback
    reason="the scope job ended '${result:-unknown}'"
  fi
  case "$mode" in
    full) why="full — $reason" ;;
    scoped) why="scoped — $reason" ;;
    *)
      mode=fallback
      why="fallback to full — ${reason:-no plan}"
      ;;
  esac
  echo "## What this run tests"
  echo
  echo "Plan: **$why**."
  if [ "$mode" = scoped ]; then
    echo
    if [ -n "${SCOPE_TOUCHED:-}" ]; then
      # shellcheck disable=SC2086 # one import path per word
      echo "Go packages whose code changed — in scope with every package that depends on them: $(printf '`%s` ' ${SCOPE_TOUCHED})"
    else
      echo "No Go package's code changed."
    fi
    if [ -n "${SCOPE_TESTS:-}" ]; then
      echo
      # shellcheck disable=SC2086 # one import path per word
      echo "Go packages whose tests alone are in scope (a test file, testdata, a file a test reads): $(printf '`%s` ' ${SCOPE_TESTS})"
    fi
    echo
    echo "Everything else is out of scope for this pull request; the merge queue, \`main\` and the nightly run test it."
  fi
  echo
  echo "| lane | state | job | note |"
  echo "|---|---|---|---|"
  local name decision res note
  while IFS='|' read -r name decision res note; do
    [ -n "$name" ] || continue
    must=1
    if [ "$mode" = scoped ] && [ "$decision" = skip ]; then must=0; fi
    if [ "$must" -eq 1 ]; then
      case "$mode:$decision" in
        *:full) state="ran in full (never scoped)" ;;
        full:*) state="ran in full" ;;
        fallback:*) state="fallback to full" ;;
        *) state="ran (scoped)" ;;
      esac
      if [ "$res" = skipped ]; then
        state="**skipped although it had to run**"
        fail=1
      fi
    elif [ "$res" = skipped ]; then
      if [ -n "${SCOPE_TOUCHED:-}${SCOPE_TESTS:-}" ]; then
        state="skipped by scope: none of the touched packages reaches it"
      else
        state="skipped by scope: no Go package touched"
      fi
    else
      state="ran although out of scope"
    fi
    echo "| $name | $state | ${res:-?} | ${note:-} |"
  done <<<"${SCOPE_LANES:-}"
  if [ "$fail" -ne 0 ]; then
    echo
    echo "A lane that had to run was skipped: this run did not test what it should have."
  fi
  return "$fail"
}

rules() {
  echo "# fallback"
  fallback_rules
  echo "# readers"
  reader_rules
  echo "# go-inert"
  go_inert_rules
  echo "# inert"
  inert_rules
}

[ "$#" -ge 1 ] || usage
cmd="$1"
shift
case "$cmd" in
  plan) plan "$@" ;;
  affected) affected "$@" ;;
  lane) lane "$@" ;;
  bazel) bazel_scope "$@" ;;
  pkgs) pkgs "$@" ;;
  go-test) go_test "$@" ;;
  keep) keep "$@" ;;
  summary) summary "$@" ;;
  rules) rules ;;
  *) usage ;;
esac
