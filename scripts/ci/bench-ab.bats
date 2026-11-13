#!/usr/bin/env bats
# BATS tests for the A/B bench lane of the performance lot
# (.github/workflows/bench-ab.yml): the table and its thresholds
# (bench-ab.conf, read by bench-ab-conf.sh), the verdict's rule — the bound of
# the 95 % interval of the per-round values against the threshold —
# (bench-ab-verdict.sh) on synthetic rounds, the interleaving of the two
# binaries (bench-ab-run.sh) and the base's build with the head's benchmarks
# (bench-ab-build.sh) against stubs, the trees (bench-ab-tree.sh) in
# throwaway repositories, step 0 (bench-ab-step0.sh), and the workflow's
# shape. Run by `make ci-scripts-check`.

setup() {
  CI_DIR="$(cd "$BATS_TEST_DIRNAME" && pwd)"
  REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
  WF="$REPO_ROOT/.github/workflows/bench-ab.yml"
  CONF="$CI_DIR/bench-ab.conf"
  WORK="$BATS_TEST_TMPDIR/work"
  mkdir -p "$WORK"
  SHA0=0123456789abcdef0123456789abcdef01234567
}

# refute <command>... — fails when the command succeeds.
refute() {
  if "$@"; then
    echo "refuted, yet it held: $*"
    return 1
  fi
}

# fixture_conf <rule line>... — a table of two suites, `s` (4 CPUs) and `p`
# (needs 8), and the rules given; exported as BENCH_AB_CONF.
fixture_conf() {
  export BENCH_AB_CONF="$WORK/fixture.conf"
  {
    echo "# a fixture"
    echo "suite s normal internal/x $SHA0 ^Benchmark 100x 4 1 ubuntu-latest - internal/x/"
    echo "suite p normal internal/x $SHA0 ^Benchmark 100x 1,8 8 ubuntu-latest - internal/x/"
    echo "suite big scale internal/x $SHA0 ^Benchmark.*_Scale$ 10x 4 1 ubuntu-latest K=1,2 -"
    printf '%s\n' "$@"
  } >"$BENCH_AB_CONF"
}

# put <side> <round> <row> <ns/op> [allocs/op] [hit/op] — one row of a
# `go test -bench` output into RAW/<side>-<round>.txt.
put() {
  local file
  file="$WORK/raw/$1-$(printf '%03d' "$2").txt"
  mkdir -p "$WORK/raw"
  if [ -n "${6:-}" ]; then
    printf '%s\t100\t%s ns/op\t0 B/op\t%s allocs/op\t%s hit/op\n' "$3" "$4" "${5:-0}" "$6" >>"$file"
  else
    printf '%s\t100\t%s ns/op\t0 B/op\t%s allocs/op\n' "$3" "$4" "${5:-0}" >>"$file"
  fi
}

# verdict_of <rule id> — the verdict column of the rule in verdict.tsv.
verdict_of() { awk -F'\t' -v id="$1" '$1 == id { print $13 }' "$WORK/out/verdict.tsv"; }
# column_of <rule id> <n> — another column of it.
column_of() { awk -F'\t' -v id="$1" -v c="$2" '$1 == id { print $c }' "$WORK/out/verdict.tsv"; }

judge() {
  run env BENCH_AB_CPUS="${CPUS:-4}" bash "$CI_DIR/bench-ab-verdict.sh" "$1" "$WORK/raw" "$WORK/out"
}

# ── the table ───────────────────────────────────────────────────────────────

@test "conf: the shipped table is well formed, and every suite measures a package of this repository" {
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 0 ]
  while read -r dir; do
    [ -d "$REPO_ROOT/$dir" ] || { echo "no package $dir"; return 1; }
    ls "$REPO_ROOT/$dir"/*_bench_test.go >/dev/null
  done < <(awk '$1 == "suite" { print $4 }' "$CONF" | sort -u)
}

@test "conf: every threshold is its issue's own, unamended" {
  # id kind metric op threshold issue — the acceptance of #1038, #1059, #808,
  # #834, #811 and #1016 as their issues wrote it (the owner's reading of
  # #808's denominator and #1059's Load_Hit included).
  expected="$(
    cat <<'EOF'
1038-set-newkey ab ns/op <= 1.05 #1038
1038-fetch-hit ab ns/op <= 1.05 #1038
1038-fetch-hit-ttl ab ns/op <= 1.05 #1038
834-fetch-hit-s3fifo diff ns/op <= 15 #834
834-zipf-s1.0-cap10 diff hit/op >= 0.05 #834
834-scan-cap10 diff hit/op >= 0.10 #834
808-striped-parallel ratio ns/op <= 2 #808
1059-fetch-hit ab ns/op <= 1.10 #1059
1059-load-hit ab ns/op <= 1.10 #1059
1059-fetch-hit-allocs value allocs/op <= 0 #1059
808-shards8-parallel ratio ns/op <= 2 #808
808-invalidate-100k ab ns/op <= 1.3 #808
811-depth-ns ratio ns/op <= 2 #811
811-depth-allocs ratio allocs/op <= 2 #811
811-batch1-allocs value allocs/op <= 200 #811
1016-text-8bound ratio ns/op <= 1.10 #1016
1016-text-32bound ratio ns/op <= 1.10 #1016
1016-json-8bound ratio ns/op <= 1.10 #1016
1016-json-32bound ratio ns/op <= 1.10 #1016
EOF
  )"
  actual="$(awk '$1 == "rule" { print $2, $4, $5, $8, $9, $10 }' "$CONF")"
  [ "$actual" = "$expected" ]
}

@test "conf: the rows the rules read are the rows the suites run" {
  grep -q '^suite kcache-par .* 1,2,4,8 8 ' "$CONF"
  grep -q '^suite dcache-par .* 1,8 8 ' "$CONF"
  grep -q '^rule 808-striped-parallel .* BenchmarkStriped_Fetch_Parallel-8 *BenchmarkFetch_Hit ' "$CONF"
  grep -q '^rule 808-shards8-parallel .* BenchmarkFetch_Parallel/stripes=8-8 *BenchmarkFetch_Hit ' "$CONF"
  grep -q '^rule 811-depth-ns .* BenchmarkReceiveBacklog/file/depth1000-4 *BenchmarkReceiveBacklog/file/depth10-4 ' "$CONF"
  grep -q '^rule 834-scan-cap10 .* BenchmarkHitRatio_Scan/s3fifo/cap10%-4 *BenchmarkHitRatio_Scan/lru/cap10%-4 ' "$CONF"
  # Every base is the commit the issues were audited at.
  [ "$(awk '$1 == "suite" { print $5 }' "$CONF" | sort -u)" = "ee1f48ba23cb193b13a91222a027326d91093811" ]
}

@test "conf: a malformed table is refused, naming the line" {
  for bad in \
    "rule a s nope ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a nosuite ab ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a s ratio ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a s ab ns/op BenchmarkA BenchmarkB <= 1 #1 gate" \
    "rule a s ab ns/op BenchmarkA - < 1 #1 gate" \
    "rule a s ab ns/op BenchmarkA - <= x #1 gate" \
    "rule a s ab ns/op BenchmarkA - <= 1 1 gate" \
    "rule a s ab ns/op BenchmarkA - <= 1 #1 maybe" \
    "rule a s ab ns/op BenchmarkA - <= 1 #1" \
    "suite s normal internal/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal internal/y abc ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal internal/y $SHA0 ^B 100 4 1 ubuntu-latest - internal/y/" \
    "suite t scale internal/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal pkg/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/"; do
    fixture_conf "$bad"
    run bash "$CI_DIR/bench-ab-conf.sh" check
    [ "$status" -eq 2 ] || { echo "accepted: $bad"; return 1; }
    [[ "$output" == *"fixture.conf:"* ]]
  done
  fixture_conf "rule a s ab ns/op BenchmarkA - <= 1 #1 gate" "rule a s ab ns/op BenchmarkB - <= 1 #1 gate"
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 2 ]
  [[ "$output" == *"declared twice"* ]]
}

@test "conf: a suite prints its values, the dispatch's overrides applied, the rounds bounded" {
  fixture_conf
  run bash "$CI_DIR/bench-ab-conf.sh" suite s
  [ "$status" -eq 0 ]
  [[ "$output" == *"SUITE_DIR=internal/x"* ]]
  [[ "$output" == *"SUITE_BASE=$SHA0"* ]]
  [[ "$output" == *"SUITE_ROUNDS=20"* ]]
  [[ "$output" == *"SUITE_ENV=-"* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" suite big
  [[ "$output" == *"SUITE_ROUNDS=10"* ]]
  [[ "$output" == *"SUITE_ENV=K=1,2"* ]]
  run env BENCH_AB_BASE=main BENCH_AB_PATTERN='^BenchmarkX$/y' BENCH_AB_BENCHTIME=5s BENCH_AB_ROUNDS=40 \
    bash "$CI_DIR/bench-ab-conf.sh" suite s
  [ "$status" -eq 0 ]
  [[ "$output" == *"SUITE_BASE=main"* ]]
  [[ "$output" == *'SUITE_PATTERN=^BenchmarkX$/y'* ]]
  [[ "$output" == *"SUITE_BENCHTIME=5s"* ]]
  [[ "$output" == *"SUITE_ROUNDS=40"* ]]
  for rounds in 9 201 x; do
    run env BENCH_AB_ROUNDS="$rounds" bash "$CI_DIR/bench-ab-conf.sh" suite s
    [ "$status" -eq 2 ] || { echo "rounds $rounds accepted"; return 1; }
  done
  run env BENCH_AB_BASE='a;b' bash "$CI_DIR/bench-ab-conf.sh" suite s
  [ "$status" -eq 2 ]
  run bash "$CI_DIR/bench-ab-conf.sh" suite nosuch
  [ "$status" -eq 2 ]
}

@test "plan: a dispatch takes the suites it names, by name or by package, in one mode" {
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal all
  [ "$status" -eq 0 ]
  [ "${lines[1]}" = "count=8" ]
  refute grep -q scale <<<"${lines[0]}"
  run bash "$CI_DIR/bench-ab-conf.sh" plan scale all
  [ "${lines[1]}" = "count=3" ]
  [[ "${lines[0]}" == *'"suite":"queue-scale"'* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal internal/service/data/queue/,logger
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"queue","runner":"ubuntu-latest"},{"suite":"logger","runner":"ubuntu-latest"}]}' ]
  run env BENCH_AB_RUNNER=big-box bash "$CI_DIR/bench-ab-conf.sh" plan normal kcache-par
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"kcache-par","runner":"big-box"}]}' ]
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal queue-scale
  [ "$status" -eq 2 ]
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal nosuch
  [ "$status" -eq 2 ]
  run env BENCH_AB_RUNNER='a b' bash "$CI_DIR/bench-ab-conf.sh" plan normal all
  [ "$status" -eq 2 ]
}

@test "plan: a pull request runs the suites whose paths it changed, every one when it changed the lane" {
  printf '%s\n' internal/kernel/collections/cache/cache.go README.md >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[1]}" = "count=6" ]
  [[ "${lines[0]}" == *'"suite":"dcache-tag"'* ]]
  refute grep -q '"queue"' <<<"${lines[0]}"
  printf '%s\n' internal/service/observe/logger/encoder/json.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"logger","runner":"ubuntu-latest"}]}' ]
  printf '%s\n' docs/README.md internal/service/data/cachex/a.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[]}' ]
  [ "${lines[1]}" = "count=0" ]
  printf '%s\n' scripts/ci/bench-ab-run.sh >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[1]}" = "count=8" ]
  run bash "$CI_DIR/bench-ab-conf.sh" plan scale changed "$WORK/changed"
  [ "$status" -eq 2 ]
}

# ── the verdict ─────────────────────────────────────────────────────────────

@test "verdict: the interval is benchstat's — the 6th and 15th of 20 values, the 2nd and 9th of 10" {
  fixture_conf "rule r s ab ns/op BenchmarkA-4 - <= 9 #1 gate"
  for r in $(seq 1 20); do put base "$r" BenchmarkA-4 100; put head "$r" BenchmarkA-4 $((100 + r)); done
  judge s
  [ "$status" -eq 0 ]
  [ "$(column_of r 9)" = 20 ]
  [ "$(column_of r 10)" = 1.06 ]
  [ "$(column_of r 11)" = 1.105 ]
  [ "$(column_of r 12)" = 1.15 ]
  rm -rf "$WORK/raw"
  for r in $(seq 1 10); do put base "$r" BenchmarkA-4 100; put head "$r" BenchmarkA-4 $((100 + r)); done
  judge s
  [ "$(column_of r 10)" = 1.02 ]
  [ "$(column_of r 12)" = 1.09 ]
}

@test "verdict: head over base holds on the interval's high bound, and fails on it with the median under the threshold" {
  fixture_conf "rule r s ab ns/op BenchmarkA-4 - <= 1.05 #1038 gate"
  for r in $(seq 1 20); do put base "$r" BenchmarkA-4 200; put head "$r" BenchmarkA-4 $((r <= 15 ? 202 : 220)); done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of r)" = pass ]
  [[ "$output" == *"| r | #1038 |"* ]]
  rm -rf "$WORK/raw"
  # 14 rounds at 1.00, 6 at 1.06: the median is 1.00, the 15th value 1.06.
  for r in $(seq 1 20); do put base "$r" BenchmarkA-4 100; put head "$r" BenchmarkA-4 $((r <= 14 ? 100 : 106)); done
  judge s
  [ "$status" -eq 1 ]
  [ "$(verdict_of r)" = fail ]
  [ "$(column_of r 11)" = 1 ]
  [ "$(column_of r 12)" = 1.06 ]
}

@test "verdict: paired rounds cancel a runner that drifts — the base and the head moving 25 % together" {
  fixture_conf "rule r s ab ns/op BenchmarkA-4 - <= 1.05 #1 gate"
  for r in $(seq 1 20); do
    b=$((100 + (r % 5) * 25))
    put base "$r" BenchmarkA-4 "$b"
    put head "$r" BenchmarkA-4 $((b * 102 / 100))
  done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of r)" = pass ]
}

@test "verdict: a ratio and a difference of two head rows, and a value, each against its threshold" {
  fixture_conf \
    "rule ratio s ratio ns/op BenchmarkB-4 BenchmarkA-4 <= 2 #811 gate" \
    "rule diff s diff hit/op BenchmarkS-4 BenchmarkL-4 >= 0.03 #834 gate" \
    "rule short-of s diff hit/op BenchmarkS-4 BenchmarkL-4 >= 0.05 #834 report" \
    "rule nsdiff s diff ns/op BenchmarkS-4 BenchmarkL-4 <= 15 #834 gate" \
    "rule allocs s value allocs/op BenchmarkB-4 - <= 200 #811 gate"
  for r in $(seq 1 20); do
    put head "$r" BenchmarkA-4 100 112
    put head "$r" BenchmarkB-4 190 180
    put head "$r" BenchmarkS-4 70 0 0.818
    put head "$r" BenchmarkL-4 60 0 0.779
    put base "$r" BenchmarkA-4 999
  done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of ratio)" = pass ]
  [ "$(column_of ratio 12)" = 1.9 ]
  [ "$(verdict_of diff)" = pass ]
  [ "$(column_of diff 10)" = 0.039 ]
  [ "$(verdict_of short-of)" = fail ]
  [ "$(verdict_of nsdiff)" = pass ]
  [ "$(column_of nsdiff 12)" = 10 ]
  [ "$(verdict_of allocs)" = pass ]
}

@test "verdict: a gate that does not hold fails the run, a report never does, enforce makes every rule a gate" {
  fixture_conf "rule g s value allocs/op BenchmarkA-4 - <= 0 #1059 report"
  for r in $(seq 1 10); do put head "$r" BenchmarkA-4 100 1; done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of g)" = fail ]
  [ "$(column_of g 14)" = report ]
  run env BENCH_AB_CPUS=4 BENCH_AB_ENFORCE=1 bash "$CI_DIR/bench-ab-verdict.sh" s "$WORK/raw" "$WORK/out"
  [ "$status" -eq 1 ]
  [ "$(column_of g 14)" = gate ]
  [[ "$output" == *"**fail**"* ]]
}

@test "verdict: fewer than 10 rounds, or a row absent, is missing; fewer CPUs than the suite needs is unjudged" {
  fixture_conf "rule short s ab ns/op BenchmarkA-4 - <= 1.05 #1 gate" \
    "rule absent s ratio ns/op BenchmarkZ-4 BenchmarkA-4 <= 2 #1 report" \
    "rule par p ratio ns/op BenchmarkP-8 BenchmarkA <= 2 #808 gate"
  for r in $(seq 1 9); do put base "$r" BenchmarkA-4 100; put head "$r" BenchmarkA-4 100; done
  judge s
  [ "$status" -eq 1 ]
  [ "$(verdict_of short)" = missing ]
  [ "$(verdict_of absent)" = missing ]
  for r in $(seq 1 20); do put head "$r" BenchmarkA 60; put head "$r" BenchmarkP-8 100; done
  CPUS=4 judge p
  [ "$status" -eq 1 ]
  [ "$(verdict_of par)" = unjudged ]
  [[ "$output" == *"unjudged (4 CPUs < 8)"* ]]
  CPUS=8 judge p
  [ "$status" -eq 0 ]
  [ "$(verdict_of par)" = pass ]
}

@test "verdict: a ratio over zero is 1 when both are zero, infinite otherwise" {
  fixture_conf "rule z s ab allocs/op BenchmarkA-4 - <= 1.05 #1 gate" "rule inf s ab allocs/op BenchmarkB-4 - <= 1.05 #1 report"
  for r in $(seq 1 10); do
    put base "$r" BenchmarkA-4 10 0; put head "$r" BenchmarkA-4 10 0
    put base "$r" BenchmarkB-4 10 0; put head "$r" BenchmarkB-4 10 1
  done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of z)" = pass ]
  [ "$(verdict_of inf)" = fail ]
  [ "$(column_of inf 12)" = inf ]
}

@test "verdict: a suite without a rule says so and passes; bad usage is refused" {
  fixture_conf
  mkdir -p "$WORK/raw"
  judge big
  [ "$status" -eq 0 ]
  [[ "$output" == *"(no rule)"* ]]
  run bash "$CI_DIR/bench-ab-verdict.sh" s "$WORK/nowhere" "$WORK/out"
  [ "$status" -eq 2 ]
  run bash "$CI_DIR/bench-ab-verdict.sh" nosuch "$WORK/raw" "$WORK/out"
  [ "$status" -eq 2 ]
}

# ── the rounds ──────────────────────────────────────────────────────────────

# stub_bin <path> <side> [fail-round] — a test binary that logs its side, its
# directory, its environment's K and its arguments, and prints one row.
stub_bin() {
  cat >"$1" <<EOF
#!/usr/bin/env bash
n=\$(( \$(cat "$WORK/calls" 2>/dev/null | wc -l) + 1 ))
echo "$2 \$PWD K=\${K:-} \$*" >>"$WORK/calls"
if [ -n "${3:-}" ] && [ "\$n" -ge "${3:-0}" ]; then echo "--- FAIL: BenchmarkA"; exit 1; fi
printf 'BenchmarkA-4\t100\t%s ns/op\n' "\$n"
EOF
  chmod +x "$1"
}

@test "run: rounds alternate A B then B A, each binary from its package directory with the suite's flags" {
  mkdir -p "$WORK/a" "$WORK/b"
  stub_bin "$WORK/base.test" base
  stub_bin "$WORK/head.test" head
  run env SUITE_ROUNDS=10 SUITE_PATTERN='^BenchmarkA$/x' SUITE_BENCHTIME=500x SUITE_CPU=1,8 SUITE_ENV=K=v1,v2 \
    bash "$CI_DIR/bench-ab-run.sh" "$WORK/base.test" "$WORK/a" "$WORK/head.test" "$WORK/b" "$WORK/out"
  [ "$status" -eq 0 ]
  [ "$(awk '{ print $1 }' "$WORK/calls" | tr '\n' ' ')" = "base head head base base head head base base head head base base head head base base head head base " ]
  [ "$(head -n 4 "$WORK/out/order.txt" | tr '\n' ' ')" = "001 base 001 head 002 head 002 base " ]
  grep -q "^base $WORK/a K=v1,v2 -test.run=^\\$ -test.bench=^BenchmarkA\\$/x -test.benchtime=500x -test.cpu=1,8 -test.count=1 -test.benchmem -test.timeout=60m$" "$WORK/calls"
  grep -q "^head $WORK/b " "$WORK/calls"
  [ "$(find "$WORK/out/raw" -name '*.txt' | wc -l)" -eq 20 ]
  [ "$(grep -c '^BenchmarkA-4' "$WORK/out/base.txt")" -eq 10 ]
  [ "$(grep -c '^BenchmarkA-4' "$WORK/out/head.txt")" -eq 10 ]
}

@test "run: a failing run stops the rounds, naming the round and the side; bad usage is refused" {
  mkdir -p "$WORK/a" "$WORK/b"
  stub_bin "$WORK/base.test" base
  stub_bin "$WORK/head.test" head 6
  run env SUITE_ROUNDS=10 SUITE_PATTERN=. SUITE_BENCHTIME=1x SUITE_CPU=4 \
    bash "$CI_DIR/bench-ab-run.sh" "$WORK/base.test" "$WORK/a" "$WORK/head.test" "$WORK/b" "$WORK/out"
  [ "$status" -eq 1 ]
  [[ "$output" == *"round 003, base failed"* ]] || [[ "$output" == *"round 003, head failed"* ]]
  [ "$(wc -l <"$WORK/calls")" -eq 6 ]
  for rounds in 9 201 ten; do
    run env SUITE_ROUNDS="$rounds" SUITE_PATTERN=. SUITE_BENCHTIME=1x SUITE_CPU=4 \
      bash "$CI_DIR/bench-ab-run.sh" "$WORK/base.test" "$WORK/a" "$WORK/head.test" "$WORK/b" "$WORK/out2"
    [ "$status" -eq 2 ]
  done
  run env SUITE_ROUNDS=10 SUITE_PATTERN=. SUITE_BENCHTIME=1x SUITE_CPU=4 \
    bash "$CI_DIR/bench-ab-run.sh" "$WORK/none.test" "$WORK/a" "$WORK/head.test" "$WORK/b" "$WORK/out2"
  [ "$status" -eq 2 ]
}

# ── the build ───────────────────────────────────────────────────────────────

# build_fixture — a base and a head tree with a module m and a package m/p;
# a stub go command that refuses a package any bench file of which says BROKEN,
# and otherwise writes the binary as the list of the package's bench files.
build_fixture() {
  for t in base head; do mkdir -p "$WORK/$t/m/p"; echo "module m" >"$WORK/$t/m/go.mod"; done
  echo "base a" >"$WORK/base/m/p/a_bench_test.go"
  echo "head a" >"$WORK/head/m/p/a_bench_test.go"
  echo "head b" >"$WORK/head/m/p/b_bench_test.go"
  cat >"$WORK/go" <<'EOF'
#!/usr/bin/env bash
# go test -c -o OUT PKG
echo "$PWD $GOWORK $*" >>"${STUB_LOG:?}"
out="$4" pkg="$5"
if grep -l BROKEN "$pkg"/*_bench_test.go >/dev/null 2>&1; then echo "undefined: Thing" >&2; exit 1; fi
cat "$pkg"/*_bench_test.go >"$out"
EOF
  chmod +x "$WORK/go"
  export GO="$WORK/go" STUB_LOG="$WORK/go.log"
}

@test "build: the head's bench files compile on the base, all of them, in the package's module" {
  build_fixture
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/base" m/p "$WORK/base.test" "$WORK/head"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "overlay=all" ]
  [[ "$output" == *"overlay-file=b_bench_test.go"* ]]
  [ "$(cat "$WORK/base.test")" = "$(printf 'head a\nhead b')" ]
  grep -q "^$WORK/base/m off test -c -o $WORK/base.test ./p$" "$WORK/go.log"
}

@test "build: a head file that does not compile on the base is left out, the base's own version restored" {
  build_fixture
  echo "head a BROKEN" >"$WORK/head/m/p/a_bench_test.go"
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/base" m/p "$WORK/base.test" "$WORK/head"
  [ "$status" -eq 0 ]
  [[ "$output" == *"overlay=partial"* ]]
  [[ "$output" == *"overlay-file=b_bench_test.go"* ]]
  refute grep -q 'overlay-file=a_bench_test.go' <<<"$output"
  [ "$(cat "$WORK/base/m/p/a_bench_test.go")" = "base a" ]
  [ "$(cat "$WORK/base.test")" = "$(printf 'base a\nhead b')" ]
}

@test "build: no head file compiles on the base — its own; the base alone broken fails; no overlay builds alone" {
  build_fixture
  echo "BROKEN" >"$WORK/head/m/p/a_bench_test.go"
  echo "BROKEN" >"$WORK/head/m/p/b_bench_test.go"
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/base" m/p "$WORK/base.test" "$WORK/head"
  [ "$status" -eq 0 ]
  [[ "$output" == *"overlay=none"* ]]
  [ "$(cat "$WORK/base.test")" = "base a" ]
  [ ! -e "$WORK/base/m/p/b_bench_test.go" ]
  echo "base BROKEN" >"$WORK/base/m/p/a_bench_test.go"
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/base" m/p "$WORK/base.test" "$WORK/head"
  [ "$status" -eq 1 ]
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/head" m/p "$WORK/head.test"
  [ "$status" -eq 1 ]
  echo "head a" >"$WORK/head/m/p/a_bench_test.go"
  echo "head b" >"$WORK/head/m/p/b_bench_test.go"
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/head" m/p "$WORK/head.test"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/head" nowhere "$WORK/head.test"
  [ "$status" -eq 2 ]
}

# ── the trees ───────────────────────────────────────────────────────────────

@test "tree: a base the shallow clone lacks is fetched one commit deep and checked out detached" {
  git init -q --bare "$WORK/origin.git"
  git -C "$WORK/origin.git" config uploadpack.allowAnySHA1InWant true
  git init -q "$WORK/src"
  (
    cd "$WORK/src"
    git config user.email t@example.com
    git config user.name t
    echo one >f && git add f && git commit -qm one
    echo two >f && git commit -qam two
    git push -q "$WORK/origin.git" HEAD:refs/heads/main
  )
  first="$(git -C "$WORK/src" rev-parse HEAD~1)"
  git clone -q --depth=1 --branch main "file://$WORK/origin.git" "$WORK/clone"
  cd "$WORK/clone"
  refute git cat-file -e "$first^{commit}" 2>/dev/null
  run bash "$CI_DIR/bench-ab-tree.sh" "$first" "$WORK/trees/base"
  [ "$status" -eq 0 ]
  [ "${lines[-1]}" = "$first" ]
  [ "$(cat "$WORK/trees/base/f")" = one ]
  run bash "$CI_DIR/bench-ab-tree.sh" HEAD "$WORK/trees/head"
  [ "${lines[-1]}" = "$(git rev-parse HEAD)" ]
  [ "$(cat "$WORK/trees/head/f")" = two ]
  run bash "$CI_DIR/bench-ab-tree.sh" ffffffffffffffffffffffffffffffffffffffff "$WORK/trees/x"
  [ "$status" -eq 1 ]
  run bash "$CI_DIR/bench-ab-tree.sh" '--upload-pack=x' "$WORK/trees/y"
  [ "$status" -eq 2 ]
  run bash "$CI_DIR/bench-ab-tree.sh" HEAD "$WORK/trees/head"
  [ "$status" -eq 2 ]
}

# ── step 0 and the runner ───────────────────────────────────────────────────

@test "step0: the compiler's decisions and perf stat at one commit, a runner without perf saying so, never failing" {
  build_fixture
  cat >"$WORK/go" <<'EOF'
#!/usr/bin/env bash
echo "$PWD $*" >>"${STUB_LOG:?}"
echo "./p/x.go:3:6: can inline f with cost 4" >&2
EOF
  printf '#!/bin/sh\nexit 0\n' >"$WORK/bin.test" && chmod +x "$WORK/bin.test"
  run env PERF="$WORK/no-perf" bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s0/head"
  [ "$status" -eq 0 ]
  grep -q "can inline f" "$WORK/s0/head-gcflags-m2.txt"
  grep -q "^$WORK/head/m build -gcflags=-m=2 ./p$" "$WORK/go.log"
  grep -q "unavailable" "$WORK/s0/head-perf-stat.txt"
  cat >"$WORK/perf" <<EOF
#!/usr/bin/env bash
echo "perf \$PWD \$*"
EOF
  chmod +x "$WORK/perf"
  run env PERF="$WORK/perf" SUITE_PATTERN='^BenchmarkA$' SUITE_BENCHTIME=9x SUITE_CPU=4 \
    bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s0/head"
  [ "$status" -eq 0 ]
  grep -q "^perf $WORK/head/m/p stat -e cycles,instructions,branch-misses,L1-dcache-load-misses -- $WORK/bin.test -test.run=^\\$ -test.bench=^BenchmarkA\\$ -test.benchtime=9x -test.cpu=4 " "$WORK/s0/head-perf-stat.txt"
  printf '#!/bin/sh\nexit 3\n' >"$WORK/go"
  run env PERF="$WORK/no-perf" bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s0/head"
  [ "$status" -eq 0 ]
  grep -q "the build failed" "$WORK/s0/head-gcflags-m2.txt"
}

@test "env: the runner's description names the suite, the commits and the clock source" {
  run env SUITE_NAME=kcache BASE_SHA=aaa HEAD_SHA=bbb OVERLAY=partial bash "$CI_DIR/bench-ab-env.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"suite:         kcache"* ]]
  [[ "$output" == *"base:          aaa"* ]]
  [[ "$output" == *"overlay:       partial"* ]]
  [[ "$output" == *"clocksource:"* ]]
  [[ "$output" == *"## lscpu"* ]]
}

# ── the workflow ────────────────────────────────────────────────────────────

@test "workflow: on dispatch with the lot's inputs and on the pull requests that touch a suite, never on a push" {
  for input in suites mode base head pattern benchtime rounds runner enforce; do
    grep -q "^      $input:$" "$WF" || { echo "no input $input"; return 1; }
  done
  grep -q '^  workflow_dispatch:$' "$WF"
  grep -q '^  pull_request:$' "$WF"
  refute grep -q 'pull_request_target' <(grep -v '^ *#' "$WF")
  refute grep -qE '^  (push|schedule):' "$WF"
}

@test "workflow: its pull-request paths are the table's suites' paths and the lane's own files" {
  expected="$(
    bash "$CI_DIR/bench-ab-conf.sh" paths | sed 's|$|**|'
    printf '%s\n' .github/workflows/bench-ab.yml 'scripts/ci/bench-ab*'
  )"
  actual="$(awk '/^    paths:$/ { on = 1; next } on && /^      - / { gsub(/^      - "|"$/, ""); print; next } on { exit }' "$WF")"
  [ "$actual" = "$expected" ]
}

@test "workflow: every action pinned by a full SHA, a read-only token, benchstat pinned, hosted runners from the table" {
  while read -r uses; do
    [[ "$uses" =~ @[0-9a-f]{40}\ \#\ v ]] || { echo "not pinned: $uses"; return 1; }
  done < <(grep -E '^\s+(- )?uses:' "$WF")
  grep -q '^permissions:$' "$WF"
  [ "$(grep -A1 '^permissions:$' "$WF" | tail -n 1)" = "  contents: read" ]
  refute grep -qE ': write$' "$WF"
  refute grep -q 'secrets\.' "$WF"
  grep -q 'benchstat@v0\.0\.0-[0-9]\{14\}-[0-9a-f]\{12\}$' "$WF"
  refute grep -q '@latest' "$WF"
  grep -q '^    runs-on: \${{ matrix.runner }}$' "$WF"
  refute grep -q 'self-hosted' <(grep -v '^ *#' "$WF")
  refute grep -qv '^suite .* ubuntu-latest ' <(grep '^suite ' "$CONF")
}

@test "workflow: an input reaches a script through the environment, never spliced into a command" {
  while IFS= read -r line; do
    [[ "$line" =~ ^\ +[A-Z_]+:\ \$\{\{\ inputs\.[a-z]+\ \}\}$ ]] ||
      [[ "$line" =~ ^\ +timeout-minutes:\ \$\{\{\ inputs\.mode ]] ||
      { echo "spliced: $line"; return 1; }
  done < <(grep -F '${{ inputs.' "$WF")
}

@test "workflow: the legs build, interleave, compare, attribute and judge through the lane's scripts, and publish whatever happened" {
  for script in conf tree build env run step0 verdict; do
    grep -q "bash scripts/ci/bench-ab-$script.sh" "$WF" || { echo "no step runs bench-ab-$script.sh"; return 1; }
    [ -f "$CI_DIR/bench-ab-$script.sh" ]
  done
  grep -q 'GOTOOLCHAIN: local' "$WF"
  grep -q 'base="$OUT/base.txt" head="$OUT/head.txt"' "$WF"
  grep -B1 -A2 'actions/upload-artifact@' "$WF" | grep -q 'if: \${{ !cancelled() }}'
  grep -q 'name: bench-ab-\${{ matrix.suite }}-\${{ github.run_id }}-\${{ github.run_attempt }}' "$WF"
  grep -q 'fail-fast: false' "$WF"
  grep -q 'cancel-in-progress: \${{ github.event_name == '"'"'pull_request'"'"' }}' "$WF"
}
