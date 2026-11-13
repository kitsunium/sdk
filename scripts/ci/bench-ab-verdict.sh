#!/usr/bin/env bash
#
# bench-ab-verdict.sh SUITE RAWDIR OUTDIR — judges every rule of SUITE
# (scripts/ci/bench-ab.conf) on the rounds bench-ab-run.sh wrote under RAWDIR
# (base-NNN.txt and head-NNN.txt, one `go test -bench` output a side a round),
# and writes OUTDIR/verdict.tsv and OUTDIR/verdict.md, the latter on stdout.
#
# The rule, per line of the table:
#   1. each round gives one value: ROW's head over ROW's base (ab), ROW over
#      ROW2 in the head (ratio), ROW minus ROW2 in the head (diff), or ROW in
#      the head (value) — a ratio over a zero is 1 when the numerator is zero
#      too, infinite otherwise;
#   2. the values of the rounds are summarised as benchstat summarises a
#      sample (golang.org/x/perf benchmath, AssumeNothing): the median, and the
#      distribution-free 95 % interval whose ends are the k-th smallest and the
#      k-th largest value, k the largest for which the binomial(n, 1/2) tail
#      below k is at most 2.5 % — the 2nd and 9th of 10, the 6th and 15th of
#      20 (tools/netgate computes the same interval);
#   3. the rule HOLDS when the bound of that interval meets the threshold: the
#      high end for `<=`, the low end for `>=`. A median that meets it while
#      the bound does not FAILS.
# Fewer than 10 rounds carrying the rule's rows is `missing`; a suite run on
# fewer CPUs than its MINCPUS is `unjudged`. Both fail a gate.
#
# A rule whose mode is `gate` — or every rule, with BENCH_AB_ENFORCE=1 — fails
# the run unless it holds; a `report` rule is judged and written, never
# failing it. BENCH_AB_CPUS is the CPU count the suite ran on (default nproc).
#
# Exit 0, 1 when a gating rule does not hold, 2 on bad usage or a bad table.

set -euo pipefail

CI_DIR="$(cd "$(dirname "$0")" && pwd)"

die() {
  printf 'bench-ab-verdict: %s\n' "$*" >&2
  exit 2
}

[ $# -eq 3 ] || die "usage: bench-ab-verdict.sh SUITE RAWDIR OUTDIR"
suite="$1" raw="$2" out="$3"
[ -d "$raw" ] || die "no directory '$raw'"
mkdir -p "$out"

rules_file="$out/rules.tsv"
bash "$CI_DIR/bench-ab-conf.sh" rules "$suite" >"$rules_file" || exit 2
mincpus="$(bash "$CI_DIR/bench-ab-conf.sh" suite "$suite" | sed -n 's/^SUITE_MINCPUS=//p')" || exit 2
cpus="${BENCH_AB_CPUS:-$(nproc)}"
[[ "$cpus" =~ ^[0-9]+$ ]] || die "BENCH_AB_CPUS '$cpus'"
enforce="${BENCH_AB_ENFORCE:-0}"
[ "$enforce" = 1 ] || [ "$enforce" = true ] && enforce=1 || enforce=0

shopt -s nullglob
files=("$raw"/base-*.txt "$raw"/head-*.txt)
shopt -u nullglob

status=0
awk -v cpus="$cpus" -v mincpus="$mincpus" -v enforce="$enforce" -v suite="$suite" \
  -v tsv="$out/verdict.tsv" -v rulesfile="$rules_file" '
BEGIN {
  FS = "[ \t]+"
  MIN_N = 10; TAIL = 0.025; INF = 1e300
}
FILENAME == rulesfile {
  split($0, f, "\t")
  nr++
  id[nr] = f[1]; kind[nr] = f[3]; metric[nr] = f[4]; row[nr] = f[5]; row2[nr] = f[6]
  op[nr] = f[7]; thr[nr] = f[8] + 0; thrs[nr] = f[8]; issue[nr] = f[9]; mode[nr] = f[10]
  next
}
FNR == 1 {
  base = FILENAME; sub(/.*\//, "", base); sub(/\.txt$/, "", base)
  side = base; sub(/-.*/, "", side)
  rnd = base; sub(/^[a-z]+-/, "", rnd)
  rounds[rnd] = 1
}
/^Benchmark/ {
  for (i = 3; i < NF; i += 2) V[side SUBSEP rnd SUBSEP $1 SUBSEP $(i + 1)] = $i + 0
}
function has(s, r, name, m) { return (s SUBSEP r SUBSEP name SUBSEP m) in V }
function val(s, r, name, m) { return V[s SUBSEP r SUBSEP name SUBSEP m] }
function quotient(a, b) { if (b == 0) return (a == 0 ? 1 : INF); return a / b }
function fmt(x) { if (x >= INF) return "inf"; return sprintf("%.6g", x) }
END {
  printf "| rule | issue | value | n | low | median | high | threshold | verdict | mode |\n"
  printf "|---|---|---|---|---|---|---|---|---|---|\n"
  printf "id\tissue\tkind\tmetric\trow\trow2\top\tthreshold\tn\tlow\tmedian\thigh\tverdict\tmode\n" > tsv
  failed = 0
  for (r = 1; r <= nr; r++) {
    n = 0
    for (rd in rounds) {
      m = metric[r]
      if (kind[r] == "ab") {
        if (!has("head", rd, row[r], m) || !has("base", rd, row[r], m)) continue
        x = quotient(val("head", rd, row[r], m), val("base", rd, row[r], m))
      } else if (kind[r] == "ratio") {
        if (!has("head", rd, row[r], m) || !has("head", rd, row2[r], m)) continue
        x = quotient(val("head", rd, row[r], m), val("head", rd, row2[r], m))
      } else if (kind[r] == "diff") {
        if (!has("head", rd, row[r], m) || !has("head", rd, row2[r], m)) continue
        x = val("head", rd, row[r], m) - val("head", rd, row2[r], m)
      } else {
        if (!has("head", rd, row[r], m)) continue
        x = val("head", rd, row[r], m)
      }
      s[++n] = x
    }
    # insertion sort: n is at most 200
    for (i = 2; i <= n; i++) { v = s[i]; for (j = i - 1; j >= 1 && s[j] > v; j--) s[j + 1] = s[j]; s[j + 1] = v }
    # k: the largest whose binomial(n, 1/2) tail P(X <= k-1) stays within TAIL
    k = 0; cdf = 0; p = exp(-n * log(2))
    for (j = 0; j < int(n / 2); j++) {
      cdf += p
      if (cdf > TAIL) break
      k = j + 1
      p = p * (n - j) / (j + 1)
    }
    lo = ""; med = ""; hi = ""
    if (n > 0) med = (n % 2 == 1) ? s[(n + 1) / 2] : (s[n / 2] + s[n / 2 + 1]) / 2
    if (cpus + 0 < mincpus + 0) verdict = "unjudged"
    else if (n < MIN_N || k == 0) verdict = "missing"
    else {
      lo = s[k]; hi = s[n - k + 1]
      bound = (op[r] == "<=") ? hi : lo
      verdict = (op[r] == "<=") ? (bound <= thr[r] ? "pass" : "fail") : (bound >= thr[r] ? "pass" : "fail")
    }
    gating = (mode[r] == "gate" || enforce == 1)
    if (gating && verdict != "pass") failed = 1
    what = (kind[r] == "ab") ? metric[r] " " row[r] " head/base" : \
           (kind[r] == "ratio") ? metric[r] " " row[r] " / " row2[r] : \
           (kind[r] == "diff") ? metric[r] " " row[r] " - " row2[r] : metric[r] " " row[r]
    shown = (verdict == "unjudged") ? "unjudged (" cpus " CPUs < " mincpus ")" : verdict
    printf "| %s | %s | %s | %d | %s | %s | %s | %s %s | %s | %s |\n", id[r], issue[r], what, n, \
      (lo == "" ? "-" : fmt(lo)), (med == "" ? "-" : fmt(med)), (hi == "" ? "-" : fmt(hi)), \
      op[r], thrs[r], (gating ? "**" shown "**" : shown), (gating ? "gate" : "report")
    printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", id[r], issue[r], kind[r], metric[r], \
      row[r], row2[r], op[r], thrs[r], n, (lo == "" ? "-" : fmt(lo)), (med == "" ? "-" : fmt(med)), \
      (hi == "" ? "-" : fmt(hi)), verdict, (gating ? "gate" : "report") > tsv
    delete s
  }
  if (nr == 0) printf "| (no rule) | - | suite %s carries no threshold | 0 | - | - | - | - | - | - |\n", suite
  exit failed
}' "$rules_file" "${files[@]:+${files[@]}}" >"$out/verdict.body" || status=$?

[ "$status" -le 1 ] || die "the judge failed (exit $status)"

{
  printf '### bench A/B: %s\n\n' "$suite"
  printf '%s rounds read from %s; %s CPUs (the suite needs %s). A rule holds when the bound of the 95 %% interval of its per-round values meets its threshold.\n\n' \
    "$(printf '%s\n' "${files[@]:+${files[@]}}" | sed -n 's/.*-\([0-9]*\)\.txt$/\1/p' | sort -u | wc -l | tr -d ' ')" \
    "$raw" "$cpus" "$mincpus"
  cat "$out/verdict.body"
} | tee "$out/verdict.md"
rm -f "$out/verdict.body"
exit "$status"
