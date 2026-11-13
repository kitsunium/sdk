#!/usr/bin/env bash
#
# bench-ab-conf.sh — reads scripts/ci/bench-ab.conf, the table of the A/B
# bench lane (.github/workflows/bench-ab.yml): its suites, and the threshold of
# every row an issue names.
#
#   bench-ab-conf.sh check
#       validates every record; exit 2 naming each line refused
#   bench-ab-conf.sh suite NAME
#       the suite as KEY=VALUE lines for $GITHUB_ENV, the dispatch's overrides
#       applied: BENCH_AB_BASE, BENCH_AB_PATTERN, BENCH_AB_BENCHTIME and
#       BENCH_AB_ROUNDS replace the suite's when set (ROUNDS defaults to 20, 10
#       in scale mode, and must lie within 10..200)
#   bench-ab-conf.sh rules NAME
#       the suite's rules, one a line, fields separated by tabs
#   bench-ab-conf.sh plan MODE SELECT [CHANGED]
#       the matrix of the run, `matrix=<json>` and `count=<n>` for
#       $GITHUB_OUTPUT: MODE normal or scale; SELECT `all`, a comma-separated
#       list of suite names or package directories, or `changed`, which reads
#       the file CHANGED (one changed path a line) and takes every normal suite
#       one of whose PATHS prefixes a changed path — every normal suite when the
#       lane's own files changed. BENCH_AB_RUNNER replaces each suite's runner
#   bench-ab-conf.sh paths
#       the path prefixes that run a suite on a pull request, sorted, one a line
#
# BENCH_AB_CONF names another table (the suite's fixtures). Exit 0, 2 on a
# malformed table or bad usage.

set -euo pipefail

CONF="${BENCH_AB_CONF:-$(cd "$(dirname "$0")" && pwd)/bench-ab.conf}"

# The lane's own files: a change to one runs every normal suite.
LANE_FILES=(.github/workflows/bench-ab.yml scripts/ci/bench-ab)

die() {
  printf 'bench-ab-conf: %s\n' "$*" >&2
  exit 2
}

[ -r "$CONF" ] || die "cannot read $CONF"

# records <kind> — the table's records of that kind, comments (a line whose
# first field opens with #) and blank lines dropped, fields separated by single
# spaces, prefixed by their line number.
records() {
  awk -v want="$1" '{
    if (NF == 0 || $1 ~ /^#/ || $1 != want) next
    line = FNR
    for (i = 2; i <= NF; i++) line = line " " $i
    print line
  }' "$CONF"
}

check() {
  local bad=0 n name mode dir base pattern benchtime cpu mincpus runner env paths
  local -A suites=() ids=()
  while read -r n name mode dir base pattern benchtime cpu mincpus runner env paths extra; do
    local why=""
    if [ -z "$paths" ] || [ -n "$extra" ]; then why="a suite has 11 fields after its keyword"
    elif ! [[ "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then why="suite name '$name'"
    elif [ -n "${suites[$name]:-}" ]; then why="suite '$name' declared twice"
    elif [ "$mode" != normal ] && [ "$mode" != scale ]; then why="mode '$mode' (normal or scale)"
    elif ! [[ "$dir" =~ ^internal(/[A-Za-z0-9_.-]+)+$ ]]; then why="directory '$dir' (a package under internal/)"
    elif ! [[ "$base" =~ ^[0-9a-f]{40}$ ]]; then why="base '$base' (a full commit SHA)"
    elif ! [[ "$benchtime" =~ ^[0-9]+(x|s|ms)$ ]]; then why="benchtime '$benchtime'"
    elif ! [[ "$cpu" =~ ^[0-9]+(,[0-9]+)*$ ]]; then why="cpu '$cpu'"
    elif ! [[ "$mincpus" =~ ^[0-9]+$ ]]; then why="mincpus '$mincpus'"
    elif ! [[ "$runner" =~ ^[A-Za-z0-9._-]+$ ]]; then why="runner '$runner'"
    elif [ "$env" != - ] && ! [[ "$env" =~ ^[A-Z_][A-Z0-9_]*=[^[:space:]]*$ ]]; then why="env '$env' (- or NAME=VALUE)"
    elif [ "$paths" != - ] && ! [[ "$paths" =~ ^[A-Za-z0-9_./-]+/(,[A-Za-z0-9_./-]+/)*$ ]]; then why="paths '$paths' (- or directories ending in /, comma-separated)"
    elif [ "$mode" = scale ] && [ "$paths" != - ]; then why="a scale suite never runs on a pull request: paths must be -"
    fi
    if [ -n "$why" ]; then
      printf 'bench-ab-conf: %s:%s: %s\n' "$CONF" "$n" "$why" >&2
      bad=1
    fi
    suites[$name]=1
  done < <(records suite)
  [ "${#suites[@]}" -gt 0 ] || { printf 'bench-ab-conf: %s declares no suite\n' "$CONF" >&2; bad=1; }

  local id suite kind metric row row2 op threshold issue rmode
  while read -r n id suite kind metric row row2 op threshold issue rmode extra; do
    local why=""
    if [ -z "$rmode" ] || [ -n "$extra" ]; then why="a rule has 10 fields after its keyword"
    elif ! [[ "$id" =~ ^[a-z0-9][a-z0-9.-]*$ ]]; then why="rule id '$id'"
    elif [ -n "${ids[$id]:-}" ]; then why="rule '$id' declared twice"
    elif [ -z "${suites[$suite]:-}" ]; then why="rule '$id' names no suite '$suite'"
    elif ! [[ "$kind" =~ ^(ab|ratio|diff|value)$ ]]; then why="kind '$kind' (ab, ratio, diff or value)"
    elif ! [[ "$metric" =~ ^[A-Za-z%-]+/[A-Za-z]+$ ]]; then why="metric '$metric'"
    elif ! [[ "$row" =~ ^Benchmark[^[:space:]]+$ ]]; then why="row '$row'"
    elif [[ "$kind" =~ ^(ratio|diff)$ ]] && ! [[ "$row2" =~ ^Benchmark[^[:space:]]+$ ]]; then why="a $kind rule needs a second row"
    elif [[ "$kind" =~ ^(ab|value)$ ]] && [ "$row2" != - ]; then why="an $kind rule takes no second row"
    elif [ "$op" != "<=" ] && [ "$op" != ">=" ]; then why="operator '$op' (<= or >=)"
    elif ! [[ "$threshold" =~ ^-?[0-9]+(\.[0-9]+)?$ ]]; then why="threshold '$threshold'"
    elif ! [[ "$issue" =~ ^#[0-9]+$ ]]; then why="issue '$issue' (#<number>)"
    elif [ "$rmode" != gate ] && [ "$rmode" != report ]; then why="mode '$rmode' (gate or report)"
    fi
    if [ -n "$why" ]; then
      printf 'bench-ab-conf: %s:%s: %s\n' "$CONF" "$n" "$why" >&2
      bad=1
    fi
    ids[$id]=1
  done < <(records rule)
  [ "$bad" -eq 0 ] || exit 2
}

# field <name> — the suite's record, without its line number; empty when absent.
suite_record() {
  records suite | awk -v name="$1" '$2 == name { $1 = ""; sub(/^ /, ""); print; exit }'
}

suite() {
  local name="${1:-}"
  [ -n "$name" ] || die "usage: bench-ab-conf.sh suite NAME"
  check
  local rec
  rec="$(suite_record "$name")"
  [ -n "$rec" ] || die "no suite '$name' in $CONF"
  local _ mode dir base pattern benchtime cpu mincpus runner env paths
  read -r _ mode dir base pattern benchtime cpu mincpus runner env paths <<<"$rec"

  local rounds=20
  [ "$mode" = scale ] && rounds=10
  rounds="${BENCH_AB_ROUNDS:-$rounds}"
  [[ "$rounds" =~ ^[0-9]+$ ]] && [ "$rounds" -ge 10 ] && [ "$rounds" -le 200 ] ||
    die "rounds '$rounds': an integer within 10..200 (fewer bound no 95 % interval worth a verdict)"
  base="${BENCH_AB_BASE:-$base}"
  [[ "$base" =~ ^[A-Za-z0-9._/-]+$ ]] || die "base '$base': a commit or a ref name"
  pattern="${BENCH_AB_PATTERN:-$pattern}"
  [[ "$pattern" != *$'\n'* ]] || die "a pattern spans no line"
  benchtime="${BENCH_AB_BENCHTIME:-$benchtime}"
  [[ "$benchtime" =~ ^[0-9]+(x|s|ms)$ ]] || die "benchtime '$benchtime'"

  printf 'SUITE_NAME=%s\nSUITE_MODE=%s\nSUITE_DIR=%s\nSUITE_BASE=%s\nSUITE_PATTERN=%s\n' \
    "$name" "$mode" "$dir" "$base" "$pattern"
  printf 'SUITE_BENCHTIME=%s\nSUITE_CPU=%s\nSUITE_MINCPUS=%s\nSUITE_ENV=%s\nSUITE_ROUNDS=%s\n' \
    "$benchtime" "$cpu" "$mincpus" "$env" "$rounds"
}

rules() {
  local name="${1:-}"
  [ -n "$name" ] || die "usage: bench-ab-conf.sh rules NAME"
  check
  [ -n "$(suite_record "$name")" ] || die "no suite '$name' in $CONF"
  records rule | awk -v name="$1" 'BEGIN { OFS = "\t" } $3 == name { $1 = ""; sub(/^\t/, ""); print }'
}

# lane_file <path> — whether the path is one of the lane's own files.
lane_file() {
  local f
  for f in "${LANE_FILES[@]}"; do
    case "$1" in "$f"*) return 0 ;; esac
  done
  return 1
}

plan() {
  local mode="${1:-}" select="${2:-}" changed="${3:-}"
  [ "$mode" = normal ] || [ "$mode" = scale ] || die "usage: bench-ab-conf.sh plan normal|scale SELECT [CHANGED]"
  [ -n "$select" ] || die "usage: bench-ab-conf.sh plan MODE SELECT [CHANGED]"
  check
  local runner_override="${BENCH_AB_RUNNER:-}"
  [ -z "$runner_override" ] || [[ "$runner_override" =~ ^[A-Za-z0-9._-]+$ ]] || die "runner '$runner_override'"

  local -a picked=()
  local n name smode dir base pattern benchtime cpu mincpus runner env paths
  if [ "$select" = changed ]; then
    [ "$mode" = normal ] || die "a pull request runs the normal suites only"
    [ -r "$changed" ] || die "changed: cannot read '$changed'"
    local all=0 path
    while IFS= read -r path; do
      [ -n "$path" ] || continue
      if lane_file "$path"; then all=1; fi
    done <"$changed"
    while read -r n name smode dir base pattern benchtime cpu mincpus runner env paths; do
      [ "$smode" = normal ] || continue
      if [ "$all" -eq 1 ]; then picked+=("$name $runner"); continue; fi
      local hit=0 prefix
      IFS=, read -r -a prefixes <<<"$paths"
      for prefix in "${prefixes[@]}"; do
        if grep -q -F -x -e "$prefix" <(cut -c1-"${#prefix}" "$changed"); then hit=1; fi
      done
      [ "$hit" -eq 0 ] || picked+=("$name $runner")
    done < <(records suite)
  else
    local -a wanted=()
    IFS=, read -r -a wanted <<<"$select"
    local w found
    for w in "${wanted[@]}"; do
      w="${w%/}"
      found=0
      while read -r n name smode dir base pattern benchtime cpu mincpus runner env paths; do
        [ "$smode" = "$mode" ] || continue
        if [ "$w" = all ] || [ "$w" = "$name" ] || [ "$w" = "$dir" ]; then
          found=1
          case " ${picked[*]:-} " in *" $name "*) ;; *) picked+=("$name $runner") ;; esac
        fi
      done < <(records suite)
      [ "$found" -eq 1 ] || die "no $mode suite is named or measures '$w'"
    done
  fi

  local json="" entry sname srunner
  for entry in "${picked[@]:+${picked[@]}}"; do
    read -r sname srunner <<<"$entry"
    [ -z "$runner_override" ] || srunner="$runner_override"
    json="${json:+$json,}{\"suite\":\"$sname\",\"runner\":\"$srunner\"}"
  done
  printf 'matrix={"include":[%s]}\ncount=%s\n' "$json" "${#picked[@]}"
}

paths() {
  check
  records suite | awk '$3 == "normal" { n = split($12, p, ","); for (i = 1; i <= n; i++) print p[i] }' | sort -u
}

cmd="${1:-}"
[ $# -gt 0 ] && shift
case "$cmd" in
  check) check ;;
  suite) suite "$@" ;;
  rules) rules "$@" ;;
  plan) plan "$@" ;;
  paths) paths ;;
  *) die "usage: bench-ab-conf.sh check|suite NAME|rules NAME|plan MODE SELECT [CHANGED]|paths" ;;
esac
