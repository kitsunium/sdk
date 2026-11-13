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
[ -d "$tree/$pkgdir" ] || die "no package directory '$tree/$pkgdir'"
mkdir -p "$(dirname "$prefix")"

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
exit 0
