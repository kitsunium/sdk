#!/usr/bin/env bash
#
# bench-ab-run.sh BASE_BIN BASE_DIR HEAD_BIN HEAD_DIR OUTDIR — the interleaved
# rounds of the A/B bench lane: ROUNDS rounds, each running the base binary
# (A) and the head binary (B) once, in the order A B, B A, A B, B A, … — so a
# drift of the runner falls on both sides and the order itself on neither.
# Each binary runs from its package directory, as `go test` runs it.
#
# Environment (bench-ab-conf.sh suite prints it):
#   SUITE_ROUNDS     rounds, 10..200
#   SUITE_PATTERN    -test.bench
#   SUITE_BENCHTIME  -test.benchtime
#   SUITE_CPU        -test.cpu
#   SUITE_ENV        `-` or one NAME=VALUE both binaries run with
#   BENCH_AB_TIMEOUT -test.timeout of one run (default 60m)
#
# Writes OUTDIR/raw/{base,head}-NNN.txt (one run's stdout each, its stderr
# beside it as .err), OUTDIR/base.txt and OUTDIR/head.txt (the rounds
# concatenated, what benchstat reads) and OUTDIR/order.txt (the runs in the
# order they ran). A run that fails stops the rounds: exit 1 naming the round
# and the side, the tail of its output on stderr; 2 on bad usage.

set -euo pipefail

die() {
  printf 'bench-ab-run: %s\n' "$*" >&2
  exit 2
}

[ $# -eq 5 ] || die "usage: bench-ab-run.sh BASE_BIN BASE_DIR HEAD_BIN HEAD_DIR OUTDIR"
base_bin="$1" base_dir="$2" head_bin="$3" head_dir="$4" out="$5"

rounds="${SUITE_ROUNDS:?SUITE_ROUNDS is required}"
pattern="${SUITE_PATTERN:?SUITE_PATTERN is required}"
benchtime="${SUITE_BENCHTIME:?SUITE_BENCHTIME is required}"
cpu="${SUITE_CPU:?SUITE_CPU is required}"
env_kv="${SUITE_ENV:--}"
timeout="${BENCH_AB_TIMEOUT:-60m}"

[[ "$rounds" =~ ^[0-9]+$ ]] && [ "$rounds" -ge 10 ] && [ "$rounds" -le 200 ] ||
  die "SUITE_ROUNDS '$rounds': an integer within 10..200"
[ "$env_kv" = - ] || [[ "$env_kv" =~ ^[A-Z_][A-Z0-9_]*=[^[:space:]]*$ ]] || die "SUITE_ENV '$env_kv'"
for bin in "$base_bin" "$head_bin"; do [ -x "$bin" ] || die "not an executable: '$bin'"; done
for dir in "$base_dir" "$head_dir"; do [ -d "$dir" ] || die "not a directory: '$dir'"; done

mkdir -p "$out/raw"
: >"$out/order.txt"

# one <side> <round> — one run of one side, its stdout kept.
one() {
  local side="$1" round="$2" bin dir file
  if [ "$side" = base ]; then bin="$base_bin" dir="$base_dir"; else bin="$head_bin" dir="$head_dir"; fi
  file="$out/raw/$side-$round.txt"
  local -a envs=()
  [ "$env_kv" = - ] || envs=("$env_kv")
  echo "$round $side" >>"$out/order.txt"
  if ! (cd "$dir" && env ${envs[@]+"${envs[@]}"} "$bin" -test.run='^$' -test.bench="$pattern" \
    -test.benchtime="$benchtime" -test.cpu="$cpu" -test.count=1 -test.benchmem \
    -test.timeout="$timeout") >"$file" 2>"$file.err"; then
    printf 'bench-ab-run: round %s, %s failed:\n' "$round" "$side" >&2
    tail -n 30 "$file" "$file.err" >&2 || true
    exit 1
  fi
  if ! grep -q '^Benchmark' "$file"; then
    printf '::warning::bench-ab-run: round %s, %s: no benchmark row matched %s\n' "$round" "$side" "$pattern" >&2
  fi
}

for r in $(seq 1 "$rounds"); do
  round="$(printf '%03d' "$r")"
  printf 'bench-ab-run: round %s/%s\n' "$r" "$rounds" >&2
  if [ $((r % 2)) -eq 1 ]; then
    one base "$round"
    one head "$round"
  else
    one head "$round"
    one base "$round"
  fi
done

cat "$out"/raw/base-*.txt >"$out/base.txt"
cat "$out"/raw/head-*.txt >"$out/head.txt"
