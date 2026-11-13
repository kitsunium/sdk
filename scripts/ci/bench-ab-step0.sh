#!/usr/bin/env bash
#
# bench-ab-step0.sh TREE PKGDIR BIN OUTPREFIX — the attribution step of the
# performance lot (step 0): what the compiler decided and what the processor
# counted, for one side, before anyone writes a fix.
#
#   OUTPREFIX-gcflags-m2.txt  `go build -gcflags=-m=2` of the package — every
#                             inlining decision and escape, in the package's
#                             module, GOWORK=off (GO names the go command)
#   OUTPREFIX-perf-stat.txt   `perf stat` of one run of BIN over the suite's
#                             rows (SUITE_PATTERN, SUITE_BENCHTIME, SUITE_CPU,
#                             SUITE_ENV), from the package directory: cycles,
#                             instructions, branch misses, L1 data misses
#
#   OUTPREFIX-<kind>.pprof    with STEP0_PROFILES (`mutex,block,cpu`, any of
#   OUTPREFIX-<kind>-top.txt  the three), one run of BIN a profile —
#                             -test.mutexprofile, -test.blockprofile,
#                             -test.cpuprofile — over STEP0_PATTERN (default
#                             SUITE_PATTERN) at the suite's benchtime, CPUs and
#                             variable, and `go tool pprof -top` of each: what
#                             holds a lock, what waits on one, what the
#                             scheduler costs, before anyone writes a fix. The
#                             suite's `step0` record in bench-ab.conf asks for
#                             them, bench-ab.yml hands them down; the .pprof
#                             keeps every stack (`-focus=` splits the rows one
#                             run measured together)
#
# Both when the runner allows: a step that cannot run writes why into its file
# instead (perf absent — PERF names it, default perf —, counters the
# hypervisor does not expose, a package that does not build). The step is a
# record, never a verdict: exit 0 whatever it found, 2 on bad usage.

set -uo pipefail

die() {
  printf 'bench-ab-step0: %s\n' "$*" >&2
  exit 2
}

[ $# -eq 4 ] || die "usage: bench-ab-step0.sh TREE PKGDIR BIN OUTPREFIX"
tree="$1" pkgdir="${2%/}" bin="$3" prefix="$4"
GO="${GO:-go}"
PERF="${PERF:-perf}"
profiles="${STEP0_PROFILES:-}"
if [ -n "$profiles" ] && ! [[ "$profiles" =~ ^(mutex|block|cpu)(,(mutex|block|cpu))*$ ]]; then
  die "STEP0_PROFILES '$profiles': mutex, block or cpu, comma-separated"
fi
[ -d "$tree/$pkgdir" ] || die "no package directory '$tree/$pkgdir'"
mkdir -p "$(dirname "$prefix")"
case "$prefix" in /*) ;; *) prefix="$PWD/$prefix" ;; esac
case "$bin" in /*) ;; *) bin="$PWD/$bin" ;; esac

mod="$pkgdir"
while [ ! -f "$tree/$mod/go.mod" ]; do
  [ "$mod" != . ] || die "no go.mod above '$pkgdir' in '$tree'"
  mod="$(dirname "$mod")"
done
if [ "$mod" = . ]; then rel="./$pkgdir"
elif [ "$mod" = "$pkgdir" ]; then rel=.
else rel="./${pkgdir#"$mod"/}"
fi

m2="$prefix-gcflags-m2.txt"
{
  echo "# go build -gcflags=-m=2 $rel (module $mod)"
  (cd "$tree/$mod" && GOWORK=off "$GO" build -gcflags=-m=2 "$rel" 2>&1) || echo "# the build failed: the record above is partial"
} >"$m2"

stat="$prefix-perf-stat.txt"
events="cycles,instructions,branch-misses,L1-dcache-load-misses"
if ! command -v "$PERF" >/dev/null 2>&1; then
  echo "perf stat: unavailable on this runner ($PERF is not installed)" >"$stat"
elif [ ! -x "$bin" ]; then
  echo "perf stat: no binary '$bin'" >"$stat"
else
  env_kv="${SUITE_ENV:--}"
  envs=()
  [ "$env_kv" = - ] || envs=("$env_kv")
  {
    echo "# perf stat -e $events, one run of the suite's rows"
    (cd "$tree/$pkgdir" && env ${envs[@]+"${envs[@]}"} "$PERF" stat -e "$events" -- "$bin" -test.run='^$' \
      -test.bench="${SUITE_PATTERN:-.}" -test.benchtime="${SUITE_BENCHTIME:-1s}" -test.cpu="${SUITE_CPU:-1}" \
      -test.count=1 -test.benchmem -test.timeout=60m 2>&1) ||
      echo "perf stat: the run failed (the counters may be absent on this hypervisor)"
  } >"$stat"
fi

# profile <kind> — one run of the binary under -test.<kind>profile, then its
# `pprof -top`; each file says why when the run or the reader fails.
profile() {
  local kind="$1" prof="$prefix-$1.pprof" top="$prefix-$1-top.txt"
  local pat="${STEP0_PATTERN:-${SUITE_PATTERN:-.}}"
  local -a envs=()
  [ "${SUITE_ENV:--}" = - ] || envs=("$SUITE_ENV")
  if [ ! -x "$bin" ]; then
    echo "$kind profile: no binary '$bin'" >"$top"
    return 0
  fi
  if ! (cd "$tree/$pkgdir" && env ${envs[@]+"${envs[@]}"} "$bin" -test.run='^$' -test.bench="$pat" \
    -test.benchtime="${SUITE_BENCHTIME:-1s}" -test.cpu="${SUITE_CPU:-1}" -test.count=1 \
    -test.timeout=60m "-test.${kind}profile=$prof" >"$prefix-$kind-run.txt" 2>&1); then
    echo "$kind profile: the run failed (see $(basename "$prefix")-$kind-run.txt)" >"$top"
    return 0
  fi
  {
    echo "# go tool pprof -top, the $kind profile of one run over $pat"
    "$GO" tool pprof -top -nodecount=40 "$bin" "$prof" 2>&1 || echo "# pprof failed: the profile above is partial"
  } >"$top"
}

if [ -n "$profiles" ]; then
  IFS=, read -r -a kinds <<<"$profiles"
  for kind in "${kinds[@]}"; do profile "$kind"; done
fi
exit 0
