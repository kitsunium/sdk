#!/usr/bin/env bats
# BATS tests for the A/B bench lane of the performance lot
# (.github/workflows/bench-ab.yml): the table and its thresholds
# (bench-ab.conf, read by bench-ab-conf.sh), the verdict's rule — the bound of
# the 95 % interval of the per-round values against the threshold, the
# processor model heading it —
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

@test "conf: every threshold is its issue's own, or raised under the owner's rule of 2026-11-14" {
  # id kind metric op threshold issue — the acceptance of #1038, #1059, #808,
  # #834, #811, #1016, #1035, #827, #840, #1034, #1102, #910, #892 and #912 as
  # their issues wrote it (the owner's reading of #808's denominator and
  # #1059's Load_Hit included; #827's and #840's per-message bounds as sixteen
  # messages' ns/op, 16 × 0.55 and 16 × 0.40; #1035's "unchanged within noise"
  # read as ±10 %, a reading to confirm with the owner; #1102's zero
  # allocations and its 1.5 x B/op bound; #910's four allocations fewer, a
  # delta of -4; #892's 10 % of the block form, a ratio of 1.10, and its two
  # floor rows; #912's loop-turn p99 "not worse than before", head over base
  # at most 1.00, and its bytes "divided by the follower count", at most 1/N;
  # #920's commit p99 "within 10 % of the no-stream baseline while a paced
  # stream runs", B over A at most 1.10, at the rate it is judged at and, for
  # information, at 2 and 32 MiB/s); #834's three bounds applied to W-TinyLFU too, the owner's rule
  # of 2026-11-14, point 3).
  expected="$(
    cat <<'EOF'
1038-set-newkey ab ns/op <= 1.05 #1038
1038-fetch-hit ab ns/op <= 1.05 #1038
1038-fetch-hit-ttl ab ns/op <= 1.05 #1038
834-fetch-hit-s3fifo diff ns/op <= 15 #834
834-zipf-s1.0-cap10 diff hit/op >= 0.05 #834
834-scan-cap10 diff hit/op >= 0.10 #834
834-fetch-hit-tinylfu diff ns/op <= 15 #834
834-tinylfu-zipf-s1.0-cap10 diff hit/op >= 0.05 #834
834-tinylfu-scan-cap10 diff hit/op >= 0.10 #834
808-striped-parallel ratio ns/op <= 2 #808
1059-fetch-hit ab ns/op <= 1.10 #1059
1059-load-hit ab ns/op <= 1.10 #1059
1059-fetch-hit-allocs value allocs/op <= 0 #1059
1059-fetch-hit-1m ab ns/op <= 1.10 #1059
1059-load-hit-1m ab ns/op <= 1.10 #1059
808-shards8-parallel ratio ns/op <= 2 #808
808-invalidate-100k ab ns/op <= 1.3 #808
840-fetchmany16-per-key ratio ns/op <= 6.4 #840
840-fetchmany16-1m ratio ns/op <= 6.4 #840
811-depth-ns ratio ns/op <= 2 #811
811-depth-allocs ratio allocs/op <= 2 #811
811-batch1-allocs value allocs/op <= 200 #811
811-scale-ns ratio ns/op <= 2 #811
827-publishmany16-per-msg ratio ns/op <= 8.8 #827
1016-text-8bound ratio ns/op <= 1.10 #1016
1016-text-32bound ratio ns/op <= 1.10 #1016
1016-json-8bound ratio ns/op <= 1.10 #1016
1016-json-32bound ratio ns/op <= 1.10 #1016
1016-text-32bound-allocs value allocs/op <= 0 #1016
1016-json-32bound-allocs value allocs/op <= 0 #1016
1035-step2-vs-uncontended ratio ns/op <= 1 #1035
1035-step1-oneshard cross ns/op <= 0.80 #1035
1035-uncontended-allocs value allocs/op <= 6 #1035
1035-shared-fnruns-high ab fn-runs/op <= 1.10 #1035
1035-shared-fnruns-low ab fn-runs/op >= 0.90 #1035
1035-step2-vs-uncontended-8 ratio ns/op <= 1 #1035
1035-step1-oneshard-8 cross ns/op <= 0.80 #1035
1034-trywrite-happy ab ns/op <= 0.96 #1034
1034-tryread-happy ab ns/op <= 0.95 #1034
1034-trywrite-allocs value allocs/op <= 0 #1034
1034-tryread-allocs value allocs/op <= 0 #1034
1034-spsc-1024 ab ns/op <= 0.78 #1034
1034-floor-sc cross ns/op >= 0.50 #1034
1102-sized-gzip-random-4k-allocs value allocs/op <= 0 #1102
1102-sized-gzip-random-4k-bytes value B/op <= 0 #1102
1102-sized-gzip-text-4k-allocs value allocs/op <= 0 #1102
1102-sized-gzip-text-4k-bytes value B/op <= 0 #1102
1102-sized-gzip-random-64k-allocs value allocs/op <= 0 #1102
1102-sized-gzip-random-64k-bytes value B/op <= 0 #1102
1102-sized-gzip-text-64k-allocs value allocs/op <= 0 #1102
1102-sized-gzip-text-64k-bytes value B/op <= 0 #1102
1102-sized-flate-random-4k-allocs value allocs/op <= 0 #1102
1102-sized-flate-random-4k-bytes value B/op <= 0 #1102
1102-sized-flate-text-4k-allocs value allocs/op <= 0 #1102
1102-sized-flate-text-4k-bytes value B/op <= 0 #1102
1102-sized-flate-random-64k-allocs value allocs/op <= 0 #1102
1102-sized-flate-random-64k-bytes value B/op <= 0 #1102
1102-sized-flate-text-64k-allocs value allocs/op <= 0 #1102
1102-sized-flate-text-64k-bytes value B/op <= 0 #1102
1102-sized-zlib-random-4k-allocs value allocs/op <= 0 #1102
1102-sized-zlib-random-4k-bytes value B/op <= 0 #1102
1102-sized-zlib-text-4k-allocs value allocs/op <= 0 #1102
1102-sized-zlib-text-4k-bytes value B/op <= 0 #1102
1102-sized-zlib-random-64k-allocs value allocs/op <= 0 #1102
1102-sized-zlib-random-64k-bytes value B/op <= 0 #1102
1102-sized-zlib-text-64k-allocs value allocs/op <= 0 #1102
1102-sized-zlib-text-64k-bytes value B/op <= 0 #1102
1102-nil-gzip-random-4k-bytes value B/op <= 6144 #1102
1102-nil-gzip-text-4k-bytes value B/op <= 6144 #1102
1102-nil-gzip-random-64k-bytes value B/op <= 98304 #1102
1102-nil-gzip-text-64k-bytes value B/op <= 98304 #1102
1102-nil-flate-random-4k-bytes value B/op <= 6144 #1102
1102-nil-flate-text-4k-bytes value B/op <= 6144 #1102
1102-nil-flate-random-64k-bytes value B/op <= 98304 #1102
1102-nil-flate-text-64k-bytes value B/op <= 98304 #1102
1102-nil-zlib-random-4k-bytes value B/op <= 6144 #1102
1102-nil-zlib-text-4k-bytes value B/op <= 6144 #1102
1102-nil-zlib-random-64k-bytes value B/op <= 98304 #1102
1102-nil-zlib-text-64k-bytes value B/op <= 98304 #1102
910-form-small-allocs delta allocs/op <= -4 #910
910-form-record-allocs delta allocs/op <= -4 #910
892-zstd-json-compress ratio ns/op <= 1.10 #892
892-zstd-json-decompress ratio ns/op <= 1.10 #892
892-zstd-random-compress ratio ns/op <= 1.10 #892
892-zstd-random-decompress ratio ns/op <= 1.10 #892
892-s2-json-compress ratio ns/op <= 1.10 #892
892-s2-json-decompress ratio ns/op <= 1.10 #892
892-s2-random-compress ratio ns/op <= 1.10 #892
892-s2-random-decompress ratio ns/op <= 1.10 #892
892-s2-random-floor ratio ns/op <= 1.10 #892
892-s2-json-floor ratio ns/op <= 1.10 #892
912-turn-p99-2followers ab p99-us/turn <= 1.00 #912
912-turn-p99-4followers ab p99-us/turn <= 1.00 #912
912-bytes-2followers ratio B/op <= 0.50 #912
912-bytes-4followers ratio B/op <= 0.25 #912
920-stream-8mib-tmpfs ratio p99-ms/commit <= 1.10 #920
920-stream-8mib-disk ratio p99-ms/commit <= 1.10 #920
920-stream-2mib-tmpfs ratio p99-ms/commit <= 1.10 #920
920-stream-32mib-tmpfs ratio p99-ms/commit <= 1.10 #920
920-stream-2mib-disk ratio p99-ms/commit <= 1.10 #920
920-stream-32mib-disk ratio p99-ms/commit <= 1.10 #920
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
  grep -q '^rule 834-scan-cap10 .* BenchmarkHitRatio/s3fifo/scan/cap10%-4 *BenchmarkHitRatio/lru/scan/cap10%-4 ' "$CONF"
  grep -q '^rule 834-tinylfu-scan-cap10 .* BenchmarkHitRatio/tinylfu/scan/cap10%-4 *BenchmarkHitRatio/lru/scan/cap10%-4 ' "$CONF"
  # #834's protocol: every hit-ratio row four levels deep, the two judged cells, 100 million accesses.
  grep -q '^suite kcache-policy .*\^BenchmarkHitRatio\$/\^(lru|s3fifo|tinylfu)\$/\^(zipf-s1.0|scan)\$/\^cap10%\$ 100000000x ' "$CONF"
  # Step 1 of #1035 alone is a row only the head has, judged against the base's own: a cross rule.
  grep -q '^rule 1035-step1-oneshard .* cross .* BenchmarkDo_DistinctKeysParallel_OneShard-4 *BenchmarkDo_DistinctKeysParallel-4 ' "$CONF"
  grep -q '^rule 1035-step1-oneshard-8 .* cross .* BenchmarkDo_DistinctKeysParallel_OneShard-8 *BenchmarkDo_DistinctKeysParallel-8 ' "$CONF"
  grep -q '^rule 1034-floor-sc .* cross .* BenchmarkSCStorePair-4 *BenchmarkTryWrite_Happy-4 ' "$CONF"
  # The committed singleflight row is -8, judged on 8 CPUs or more; the -4 suite needs the 4 it runs on.
  grep -q '^suite sflight .* 4 4 ubuntu-latest ' "$CONF"
  grep -q '^suite sflight-8 .* 8 8 ubuntu-latest ' "$CONF"
  # A two-level -test.bench expression on a benchmark with no sub-benchmark reports nothing: two ring suites.
  grep -q '^suite ring .*Benchmark(TryWrite_Happy|TryRead_Happy|SCStorePair|PublishFloor)\$ ' "$CONF"
  grep -q '^suite ring-spsc .*BenchmarkSPSC_ProducerConsumer\$/\^size1024\$ ' "$CONF"
  # #827 runs on vfs's changes too, #840 on the port's.
  grep -q '^suite queue-publish .* internal/service/data/queue/,internal/service/data/vfs/$' "$CONF"
  grep -q '^suite dcache-bulk .* internal/core/data/cache/,internal/kernel/collections/cache/,internal/service/data/cache/$' "$CONF"
  # The three suites outside internal/service's and the kernel's: the domain's transform, the promoted codecs
  # in pkg/v1 (the form codec's engine a path too), the vendor module's streams (the port's a path too).
  grep -q '^suite transform normal internal/service/data/transform .*\^BenchmarkDecompress(Sized)?\$/\^(gzip|flate|zlib)\$/\^(random|text)\$/\^(4096|65536)\$ .* internal/service/data/transform/$' "$CONF"
  grep -q '^suite codec-promote normal pkg/v1/data/codec .*\^BenchmarkMarshalPromoted\$ .* pkg/v1/data/codec/,internal/service/data/codec/form/$' "$CONF"
  grep -q '^suite tstream normal third-party/transform .*\^Benchmark(Stream|StreamFloor)\$ .* third-party/transform/,internal/core/data/transform/$' "$CONF"
  # #910 is read head minus base; #892's ten rows are head rows against head rows, the base having no stream.
  grep -q '^rule 910-form-small-allocs  *codec-promote delta allocs/op BenchmarkMarshalPromoted/form/small-4 *- <= -4 #910 report$' "$CONF"
  [ "$(awk '$1 == "rule" && $3 == "tstream" { print $4 }' "$CONF" | sort | uniq -c | tr -s ' ' | tr '\n' ' ')" = " 10 ratio " ]
  [ "$(awk '$1 == "step0" { print $2, $3, $4 }' "$CONF" | grep '^tstream ')" = 'tstream cpu ^BenchmarkStream$/^random$/^s2$/^decompress$' ]
  # #912's turn rows run one paced run of 5 s an op on the 4 CPUs they are judged on, base against head;
  # its bytes are two head rows, the base having no BatchSender.
  grep -q '^suite cturn normal internal/service/app/consensus .*\^BenchmarkLeaderLoopTurnAt1000PerSecond\$ 1x 4 4 ubuntu-latest - internal/service/app/consensus/,internal/core/app/consensus/$' "$CONF"
  grep -q '^rule 912-turn-p99-2followers  *cturn ab  *p99-us/turn BenchmarkLeaderLoopTurnAt1000PerSecond/followers=2-4 ' "$CONF"
  grep -q '^rule 912-bytes-4followers  *cenc  ratio B/op  *BenchmarkLeaderTurnEncoding/shared/followers=4-4 BenchmarkLeaderTurnEncoding/perpeer/followers=4-4 ' "$CONF"
  [ "$(awk '$1 == "step0" { print $2, $3, $4 }' "$CONF" | grep '^cturn ')" = 'cturn cpu -' ]
  # #920's two rows are one op's, both the head's (the base has no pacing): four levels deep, the
  # fourth left open by the suite's three-level expression, 8 MiB/s judged and 2 and 32 MiB/s on
  # dispatch; each rule divides a device's and a rate's paced+stream by the same device's and rate's
  # paced, and reads a rate its suite runs.
  grep -q '^suite cstream  *normal internal/service/app/consensus .*\^BenchmarkTCPCommitStream\$/\^(data-disk|tmpfs)\$/\^rate=8MiB\$ 1x 4 4 ubuntu-latest - internal/service/app/consensus/,internal/core/app/consensus/$' "$CONF"
  grep -q '^suite cstream-scale scale  *internal/service/app/consensus .*\^BenchmarkTCPCommitStream\$/\^(data-disk|tmpfs)\$/\^rate=(2|32)MiB\$ 1x 4 4 ubuntu-latest - -$' "$CONF"
  grep -q '^rule 920-stream-8mib-tmpfs  *cstream  *ratio p99-ms/commit BenchmarkTCPCommitStream/tmpfs/rate=8MiB/paced+stream-4  *BenchmarkTCPCommitStream/tmpfs/rate=8MiB/paced-4 ' "$CONF"
  [ "$(awk '$1 == "rule" && $3 ~ /^cstream/ { print $4, $5 }' "$CONF" | sort | uniq -c | tr -s ' ' | tr '\n' ' ')" = " 6 ratio p99-ms/commit " ]
  while read -r id suite row row2; do
    [ "$row2" = "${row%/paced+stream-4}/paced-4" ] || { echo "$id divides $row by $row2"; return 1; }
    rate="${row#BenchmarkTCPCommitStream/*/rate=}" rate="${rate%%MiB/*}"
    case "$suite:$rate" in cstream:8 | cstream-scale:2 | cstream-scale:32) ;; *) echo "$id reads rate $rate, which $suite does not run"; return 1 ;; esac
  done < <(awk '$1 == "rule" && $3 ~ /^cstream/ { print $2, $3, $6, $7 }' "$CONF")
  [ "$(awk '$1 == "step0" { print $2, $3, $4 }' "$CONF" | grep '^cstream ')" = 'cstream cpu -' ]
  # A rule gates once its pull request makes it hold, and only then: #834's two S3-FIFO hit
  # ratios, the same on every run; every other rule reports.
  [ "$(awk '$1 == "rule" && $11 == "gate" { print $2 }' "$CONF" | tr '\n' ' ')" = "834-zipf-s1.0-cap10 834-scan-cap10 " ]
  [ "$(awk '$1 == "rule" && $11 != "gate" { print $11 }' "$CONF" | sort -u)" = report ]
  # Every base is the commit the issues were audited at.
  [ "$(awk '$1 == "suite" { print $5 }' "$CONF" | sort -u)" = "ee1f48ba23cb193b13a91222a027326d91093811" ]
}

@test "conf: a malformed table is refused, naming the line" {
  for bad in \
    "rule a s nope ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a nosuite ab ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a s ratio ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a s ab ns/op BenchmarkA BenchmarkB <= 1 #1 gate" \
    "rule a s cross ns/op BenchmarkA - <= 1 #1 gate" \
    "rule a s value ns/op BenchmarkA BenchmarkB <= 1 #1 gate" \
    "step0 nosuch cpu -" \
    "step0 s heap -" \
    "step0 s mutex,cpu,pprof -" \
    "step0 s mutex" \
    "step0 s mutex - extra" \
    "rule a s ab ns/op BenchmarkA - < 1 #1 gate" \
    "rule a s ab ns/op BenchmarkA - <= x #1 gate" \
    "rule a s ab ns/op BenchmarkA - <= 1 1 gate" \
    "rule a s ab ns/op BenchmarkA - <= 1 #1 maybe" \
    "rule a s ab ns/op BenchmarkA - <= 1 #1" \
    "suite s normal internal/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal internal/y abc ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal internal/y $SHA0 ^B 100 4 1 ubuntu-latest - internal/y/" \
    "suite t scale internal/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal pkg/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal pkg/v1 $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal pkg/v2/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal third-party $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal third-partyx/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal e2e/y $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "suite t normal pkg/v1/y;z $SHA0 ^B 100x 4 1 ubuntu-latest - internal/y/" \
    "rule a s delta ns/op BenchmarkA BenchmarkB <= -4 #1 gate" \
    "rule a s delta ns/op BenchmarkA - <= x #1 gate" \
    "rule a s deltas ns/op BenchmarkA - <= -4 #1 gate"; do
    fixture_conf "$bad"
    run bash "$CI_DIR/bench-ab-conf.sh" check
    [ "$status" -eq 2 ] || { echo "accepted: $bad"; return 1; }
    [[ "$output" == *"fixture.conf:"* ]]
  done
  fixture_conf "rule a s ab ns/op BenchmarkA - <= 1 #1 gate" "rule a s ab ns/op BenchmarkB - <= 1 #1 gate"
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 2 ]
  [[ "$output" == *"declared twice"* ]]
  fixture_conf "step0 s cpu -" "step0 s block -"
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 2 ]
  [[ "$output" == *"step0 of suite 's' declared twice"* ]]
}

@test "conf: a metric may carry digits, a statistic in a unit per what, and still names one per-what" {
  fixture_conf "rule t s ab p99-us/turn BenchmarkA-4 - <= 1.00 #912 report" \
    "rule u s value p50-us/turn BenchmarkA-4 - <= 5 #912 report" \
    "rule v s ratio B/op BenchmarkA-4 BenchmarkB-4 <= 0.25 #912 report"
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 0 ]
  run bash "$CI_DIR/bench-ab-conf.sh" rules s
  [[ "$output" == *"$(printf 't\ts\tab\tp99-us/turn\tBenchmarkA-4')"* ]]
  for bad in "p99-us" "p99-us/turn/op" "p99_us/turn" "p99-us/" "/turn"; do
    fixture_conf "rule t s ab $bad BenchmarkA-4 - <= 1.00 #912 report"
    run bash "$CI_DIR/bench-ab-conf.sh" check
    [ "$status" -eq 2 ] || { echo "accepted metric $bad"; return 1; }
    [[ "$output" == *"metric '$bad'"* ]]
  done
}

@test "verdict: a metric a benchmark reports itself, digits in its unit, is read like ns/op" {
  fixture_conf "rule turn s ab p99-us/turn BenchmarkTurn/followers=2-4 - <= 1.00 #912 gate" \
    "rule worse s ab p99-us/turn BenchmarkTurn/followers=2-4 - <= 0.70 #912 report"
  mkdir -p "$WORK/raw"
  for r in $(seq 1 20); do
    # 40 µs in the base, 30 µs in the head: 0.75 every round.
    printf 'BenchmarkTurn/followers=2-4\t1\t5000000000 ns/op\t2.1 p50-us/turn\t40 p99-us/turn\n' >"$WORK/raw/base-$(printf '%03d' "$r").txt"
    printf 'BenchmarkTurn/followers=2-4\t1\t5000000000 ns/op\t2.1 p50-us/turn\t30 p99-us/turn\n' >"$WORK/raw/head-$(printf '%03d' "$r").txt"
  done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of turn)" = pass ]
  [ "$(column_of turn 12)" = 0.75 ]
  [ "$(verdict_of worse)" = fail ]
}

@test "verdict: one op's two rows, four levels deep and a '+' in a name, are a ratio of the head like any other" {
  fixture_conf "rule tmp s ratio p99-ms/commit BenchmarkS/tmpfs/rate=8MiB/paced+stream-4 BenchmarkS/tmpfs/rate=8MiB/paced-4 <= 1.10 #920 gate" \
    "rule dsk s ratio p99-ms/commit BenchmarkS/data-disk/rate=8MiB/paced+stream-4 BenchmarkS/data-disk/rate=8MiB/paced-4 <= 1.10 #920 report"
  mkdir -p "$WORK/raw"
  for r in $(seq 1 20); do
    # The base carries neither row: the file does not build there. B over A, 1.05 on tmpfs and 1.25 on the disk.
    printf 'PASS\n' >"$WORK/raw/base-$(printf '%03d' "$r").txt"
    {
      printf 'BenchmarkS/tmpfs/rate=8MiB/paced-4\t1\t100 ns/op\t8000 commits/op\t2.000 p99-ms/commit\n'
      printf 'BenchmarkS/tmpfs/rate=8MiB/paced+stream-4\t1\t100 ns/op\t8000 commits/op\t2.100 p99-ms/commit\n'
      printf 'BenchmarkS/data-disk/rate=8MiB/paced-4\t1\t100 ns/op\t8000 commits/op\t10.00 p99-ms/commit\n'
      printf 'BenchmarkS/data-disk/rate=8MiB/paced+stream-4\t1\t100 ns/op\t8000 commits/op\t12.50 p99-ms/commit\n'
    } >"$WORK/raw/head-$(printf '%03d' "$r").txt"
  done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of tmp)" = pass ]
  [ "$(column_of tmp 12)" = 1.05 ]
  [ "$(verdict_of dsk)" = fail ]
  [ "$(column_of dsk 12)" = 1.25 ]
}

@test "conf: a suite measures a package of internal/, of pkg/v1/ or of a vendor module under third-party/, and a delta rule reads one row" {
  fixture_conf "suite k normal pkg/v1/data/codec $SHA0 ^B 100x 4 1 ubuntu-latest - pkg/v1/data/codec/" \
    "suite v normal third-party/db/writer/redis $SHA0 ^B 100x 4 1 ubuntu-latest - third-party/db/writer/redis/,internal/core/x/" \
    "rule d k delta allocs/op BenchmarkA-4 - <= -4 #910 report" \
    "rule e v delta B/op BenchmarkA-4 - >= 0.5 #1 gate"
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 0 ]
  run bash "$CI_DIR/bench-ab-conf.sh" suite v
  [[ "$output" == *"SUITE_DIR=third-party/db/writer/redis"* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" rules k
  [ "$output" = "$(printf 'd\tk\tdelta\tallocs/op\tBenchmarkA-4\t-\t<=\t-4\t#910\treport')" ]
  run bash "$CI_DIR/bench-ab-conf.sh" paths
  [ "$output" = "$(printf 'internal/core/x/\ninternal/x/\npkg/v1/data/codec/\nthird-party/db/writer/redis/')" ]
}

@test "conf: a cross rule reads two rows, and a step0 record names a suite and its profiles" {
  fixture_conf "rule c s cross ns/op BenchmarkOne-4 BenchmarkTwo-4 <= 0.8 #1035 report" \
    "step0 s mutex,block,cpu -" "step0 p cpu ^BenchmarkP$"
  run bash "$CI_DIR/bench-ab-conf.sh" check
  [ "$status" -eq 0 ]
  run bash "$CI_DIR/bench-ab-conf.sh" rules s
  [ "$output" = "$(printf 'c\ts\tcross\tns/op\tBenchmarkOne-4\tBenchmarkTwo-4\t<=\t0.8\t#1035\treport')" ]
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

# An awk that stops reading at its program's `exit`, as gawk does, in front of the real one: it feeds
# the program a line at a time and, once the program is gone, leaves the rest of its input unread and
# closes it. A program that never exits is fed to the end. Only the readers' (`-v name=…`) go
# through it; `records`' own awk reads a file.
early_exit_awk() {
  local real
  real="$(command -v awk)"
  mkdir -p "$WORK/bin"
  cat >"$WORK/bin/awk" <<EOF
#!/usr/bin/env bash
case " \$* " in *" name="*) ;; *) exec "$real" "\$@" ;; esac
exec 3>&1
coproc A { exec "$real" "\$@" >&3; }
n=0
while IFS= read -r line; do
  kill -0 "\$A_PID" 2>/dev/null || exit 0
  printf '%s\n' "\$line" >&"\${A[1]}" 2>/dev/null || exit 0
  n=\$((n + 1))
  [ "\$n" -gt 5 ] || sleep 0.05
done
kill -0 "\$A_PID" 2>/dev/null || exit 0
exec {A[1]}>&-
wait "\$A_PID"
EOF
  chmod +x "$WORK/bin/awk"
}

@test "conf: a reader whose awk stops at its match does not kill the table's producer with SIGPIPE (#1498)" {
  fixture_conf "step0 s cpu -"
  # More than a pipe buffer (64 KiB) of suite records after the match: the producer is still writing
  # when the reader has gone.
  local i
  for i in $(seq 1 3000); do
    echo "suite pad$i normal internal/x $SHA0 ^Benchmark 100x 4 1 ubuntu-latest - internal/x/"
  done >>"$BENCH_AB_CONF"
  [ "$(awk '$1 == "suite"' "$BENCH_AB_CONF" | wc -c)" -gt 65536 ]
  early_exit_awk
  run env PATH="$WORK/bin:$PATH" bash "$CI_DIR/bench-ab-conf.sh" suite s
  echo "$output"
  [ "$status" -eq 0 ]
  [[ "$output" == *"SUITE_NAME=s"* ]]
  [[ "$output" == *"SUITE_STEP0_PROFILES=cpu"* ]]
}

@test "conf: a suite's step0 record reaches the leg: its profiles and the rows they run over, the suite's own when -" {
  fixture_conf "step0 s mutex,block,cpu -" "step0 p cpu ^BenchmarkP\$"
  run bash "$CI_DIR/bench-ab-conf.sh" suite s
  [ "$status" -eq 0 ]
  [[ "$output" == *"SUITE_STEP0_PROFILES=mutex,block,cpu"* ]]
  [[ "$output" == *"SUITE_STEP0_PATTERN=^Benchmark"* ]]
  run env BENCH_AB_PATTERN='^BenchmarkX$' bash "$CI_DIR/bench-ab-conf.sh" suite s
  [[ "$output" == *'SUITE_STEP0_PATTERN=^BenchmarkX$'* ]]
  run env BENCH_AB_PATTERN='^BenchmarkX$' bash "$CI_DIR/bench-ab-conf.sh" suite p
  [[ "$output" == *'SUITE_STEP0_PROFILES=cpu'* ]]
  [[ "$output" == *'SUITE_STEP0_PATTERN=^BenchmarkP$'* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" suite big
  [[ "$output" == *"SUITE_STEP0_PROFILES="$'\n'* ]]
  [[ "$output" == *"SUITE_STEP0_PATTERN="* ]]
  # The shipped table asks for singleflight's profiles at both CPU counts, #892's CPU profile of the s2 stream,
  # #912's of the leader's loop turn and #920's of the commit while a snapshot streams, each over the
  # suite's own rows, and nobody else's.
  unset BENCH_AB_CONF
  for suite in sflight sflight-8; do
    run bash "$CI_DIR/bench-ab-conf.sh" suite "$suite"
    [[ "$output" == *"SUITE_STEP0_PROFILES=mutex,block,cpu"* ]]
    [[ "$output" == *'SUITE_STEP0_PATTERN=^BenchmarkDo_DistinctKeysParallel(_OneShard)?$'* ]]
  done
  [ "$(awk '$1 == "step0" { print $2 }' "$CONF" | tr '\n' ' ')" = "sflight sflight-8 tstream cturn cstream " ]
  run bash "$CI_DIR/bench-ab-conf.sh" suite tstream
  [[ "$output" == *"SUITE_STEP0_PROFILES=cpu"* ]]
  [[ "$output" == *'SUITE_STEP0_PATTERN=^BenchmarkStream$/^random$/^s2$/^decompress$'* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" suite cturn
  [[ "$output" == *"SUITE_STEP0_PROFILES=cpu"* ]]
  [[ "$output" == *'SUITE_STEP0_PATTERN=^BenchmarkLeaderLoopTurnAt1000PerSecond$'* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" suite cstream
  [[ "$output" == *"SUITE_STEP0_PROFILES=cpu"* ]]
  [[ "$output" == *'SUITE_STEP0_PATTERN=^BenchmarkTCPCommitStream$/^(data-disk|tmpfs)$/^rate=8MiB$'* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" suite cstream-scale
  [[ "$output" == *"SUITE_STEP0_PROFILES="$'\n'* ]]
  [[ "$output" == *"SUITE_ROUNDS=10"* ]]
}

@test "plan: a dispatch takes the suites it names, by name or by package, in one mode" {
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal all
  [ "$status" -eq 0 ]
  [ "${lines[1]}" = "count=20" ]
  refute grep -q scale <<<"${lines[0]}"
  run bash "$CI_DIR/bench-ab-conf.sh" plan scale all
  [ "${lines[1]}" = "count=4" ]
  [[ "${lines[0]}" == *'"suite":"queue-scale"'* ]]
  [[ "${lines[0]}" == *'"suite":"cstream-scale"'* ]]
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal internal/service/data/queue/,logger
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"queue","runner":"ubuntu-latest"},{"suite":"queue-publish","runner":"ubuntu-latest"},{"suite":"logger","runner":"ubuntu-latest"}]}' ]
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal internal/kernel/concur/singleflight
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"sflight","runner":"ubuntu-latest"},{"suite":"sflight-8","runner":"ubuntu-latest"}]}' ]
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal internal/service/app/consensus
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"cturn","runner":"ubuntu-latest"},{"suite":"cenc","runner":"ubuntu-latest"},{"suite":"cstream","runner":"ubuntu-latest"}]}' ]
  run bash "$CI_DIR/bench-ab-conf.sh" plan scale internal/service/app/consensus
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"cstream-scale","runner":"ubuntu-latest"}]}' ]
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
  [ "${lines[1]}" = "count=7" ]
  [[ "${lines[0]}" == *'"suite":"dcache-tag"'* ]]
  [[ "${lines[0]}" == *'"suite":"dcache-bulk"'* ]]
  refute grep -q '"queue"' <<<"${lines[0]}"
  # The new suites run on the packages they measure: the port of the cache, vfs under the queue's publication, the ring, singleflight.
  for pair in internal/core/data/cache/port.go:dcache-bulk internal/service/data/vfs/os_publish.go:queue-publish \
    internal/kernel/collections/ring/ring.go:ring internal/kernel/collections/ring/ring.go:ring-spsc \
    internal/kernel/concur/singleflight/singleflight.go:sflight internal/kernel/concur/singleflight/singleflight.go:sflight-8; do
    printf '%s\n' "${pair%%:*}" >"$WORK/changed"
    run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
    [[ "${lines[0]}" == *"\"suite\":\"${pair##*:}\""* ]] || { echo "${pair%%:*} ran no ${pair##*:}: ${lines[0]}"; return 1; }
  done
  printf '%s\n' internal/service/data/vfs/os_publish.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"queue-publish","runner":"ubuntu-latest"}]}' ]
  # #912's and #920's: the driver and its step function run the three suites, the consensus port the
  # turn and the stream; the stream's scale suite never runs on a pull request.
  printf '%s\n' internal/service/app/consensus/raft/raft.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"cturn","runner":"ubuntu-latest"},{"suite":"cenc","runner":"ubuntu-latest"},{"suite":"cstream","runner":"ubuntu-latest"}]}' ]
  printf '%s\n' internal/core/app/consensus/membership.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"cturn","runner":"ubuntu-latest"},{"suite":"cstream","runner":"ubuntu-latest"}]}' ]
  printf '%s\n' internal/service/app/consensus/node_pace.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [[ "${lines[0]}" == *'"suite":"cstream"'* ]]
  refute grep -q cstream-scale <<<"${lines[0]}"
  # Outside internal/: the domain's transform, the promoted codecs and the form engine under them, the vendor module and the port it streams through.
  for pair in internal/service/data/transform/gzip.go:transform pkg/v1/data/codec/codec.go:codec-promote \
    internal/service/data/codec/form/form.go:codec-promote third-party/transform/stream.go:tstream \
    internal/core/data/transform/port.go:tstream; do
    printf '%s\n' "${pair%%:*}" >"$WORK/changed"
    run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
    [ "${lines[0]}" = "matrix={\"include\":[{\"suite\":\"${pair##*:}\",\"runner\":\"ubuntu-latest\"}]}" ] || { echo "${pair%%:*}: ${lines[0]}"; return 1; }
  done
  # A neighbour that is none of their paths runs nothing: pkg/v1's other codecs' families, another vendor module.
  printf '%s\n' pkg/v1/data/transform/x.go pkg/v1/data/codecs/x.go third-party/transformer/x.go third-party/codec/yaml/x.go internal/service/data/codec/json/x.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[1]}" = "count=0" ]
  printf '%s\n' internal/service/observe/logger/encoder/json.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[{"suite":"logger","runner":"ubuntu-latest"}]}' ]
  printf '%s\n' docs/README.md internal/service/data/cachex/a.go >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[0]}" = 'matrix={"include":[]}' ]
  [ "${lines[1]}" = "count=0" ]
  printf '%s\n' scripts/ci/bench-ab-run.sh >"$WORK/changed"
  run bash "$CI_DIR/bench-ab-conf.sh" plan normal changed "$WORK/changed"
  [ "${lines[1]}" = "count=20" ]
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

@test "verdict: a delta rule is the head's row minus the base's, round by round, on the interval's bound, in the metric's own unit" {
  fixture_conf \
    "rule fewer s delta allocs/op BenchmarkA-4 - <= -4 #910 gate" \
    "rule short s delta allocs/op BenchmarkA-4 - <= -5 #910 report" \
    "rule up s delta allocs/op BenchmarkA-4 - >= -6 #910 gate" \
    "rule bytes s delta B/op BenchmarkB-4 - <= -300 #1 report"
  for r in $(seq 1 20); do
    # 14 allocs in the base, 8 to 10 in the head: the deltas are -6 .. -4; the head's own value is no delta.
    put base "$r" BenchmarkA-4 100 14
    put head "$r" BenchmarkA-4 100 $((8 + r % 3))
    printf 'BenchmarkB-4\t100\t5 ns/op\t1000 B/op\t3 allocs/op\n' >>"$WORK/raw/base-$(printf '%03d' "$r").txt"
    printf 'BenchmarkB-4\t100\t5 ns/op\t800 B/op\t3 allocs/op\n' >>"$WORK/raw/head-$(printf '%03d' "$r").txt"
  done
  judge s
  [ "$status" -eq 0 ]
  # The 15th of 20 values is the high bound: -4 holds -4, and fails -5; the low bound -6 holds -6.
  [ "$(verdict_of fewer)" = pass ]
  [ "$(column_of fewer 10)" = -6 ]
  [ "$(column_of fewer 12)" = -4 ]
  [ "$(column_of fewer 3)" = delta ]
  [ "$(verdict_of short)" = fail ]
  [ "$(verdict_of up)" = pass ]
  [ "$(verdict_of bytes)" = fail ]
  [ "$(column_of bytes 11)" = -200 ]
  [[ "$output" == *"| fewer | #910 | allocs/op BenchmarkA-4 head-base | 20 | -6 | -5 | -4 | <= -4 | **pass** | gate |"* ]]
  # A count that grew is a positive delta: it fails a bound of -4 on every round.
  rm -rf "$WORK/raw"
  for r in $(seq 1 20); do put base "$r" BenchmarkA-4 100 14; put head "$r" BenchmarkA-4 100 16; done
  judge s
  [ "$status" -eq 1 ]
  [ "$(verdict_of fewer)" = fail ]
  [ "$(column_of fewer 11)" = 2 ]
}

@test "verdict: a cross rule divides a head row by a row of the BASE, round by round, whatever the head says of that row" {
  fixture_conf \
    "rule within s cross ns/op BenchmarkOne-4 BenchmarkTwo-4 <= 0.80 #1035 gate" \
    "rule beyond s cross ns/op BenchmarkOne-4 BenchmarkTwo-4 <= 0.60 #1035 report" \
    "rule floor s cross ns/op BenchmarkOne-4 BenchmarkTwo-4 >= 0.65 #1034 report"
  for r in $(seq 1 20); do
    # The head's Two is a different, slower row: reading it would give 0.07.
    put head "$r" BenchmarkOne-4 70
    put head "$r" BenchmarkTwo-4 1000
    put base "$r" BenchmarkTwo-4 100
    put base "$r" BenchmarkOne-4 5
  done
  judge s
  [ "$status" -eq 0 ]
  [ "$(verdict_of within)" = pass ]
  [ "$(column_of within 11)" = 0.7 ]
  [ "$(verdict_of beyond)" = fail ]
  [ "$(verdict_of floor)" = pass ]
  [[ "$output" == *"ns/op BenchmarkOne-4 head / BenchmarkTwo-4 base"* ]]
  # The bound is the interval's, as for every kind: 5 rounds of 20 at 0.90, the 15th value, fail the 0.80 gate with the median at 0.70.
  rm -rf "$WORK/raw"
  for r in $(seq 1 20); do
    put head "$r" BenchmarkOne-4 $((r <= 14 ? 70 : 90))
    put base "$r" BenchmarkTwo-4 100
  done
  judge s
  [ "$status" -eq 1 ]
  [ "$(verdict_of within)" = fail ]
  [ "$(column_of within 11)" = 0.7 ]
  [ "$(column_of within 12)" = 0.9 ]
}

@test "verdict: a row the binary does not carry — a benchmark whose pull request has not landed — is missing, never a pass, for every kind" {
  # BenchmarkNew is the head's alone (its pull request adds it); BenchmarkGone is in no binary.
  fixture_conf \
    "rule ab-new s ab ns/op BenchmarkNew-4 - <= 1.05 #1 gate" \
    "rule cross-new-in-base s cross ns/op BenchmarkA-4 BenchmarkNew-4 <= 2 #1 gate" \
    "rule ratio-gone s ratio ns/op BenchmarkA-4 BenchmarkGone-4 <= 2 #1 gate" \
    "rule diff-gone s diff ns/op BenchmarkA-4 BenchmarkGone-4 <= 15 #1 gate" \
    "rule cross-gone-head s cross ns/op BenchmarkGone-4 BenchmarkA-4 <= 2 #1 gate" \
    "rule cross-gone-base s cross ns/op BenchmarkA-4 BenchmarkGone-4 <= 2 #1 gate" \
    "rule value-gone s value allocs/op BenchmarkGone-4 - <= 0 #1 gate" \
    "rule delta-new s delta allocs/op BenchmarkNew-4 - <= -4 #910 gate" \
    "rule delta-gone s delta allocs/op BenchmarkGone-4 - <= -4 #910 gate" \
    "rule exists s ratio ns/op BenchmarkNew-4 BenchmarkA-4 <= 2 #1 gate"
  for r in $(seq 1 20); do
    put head "$r" BenchmarkA-4 100
    put base "$r" BenchmarkA-4 100
    put head "$r" BenchmarkNew-4 50
  done
  judge s
  [ "$status" -eq 1 ]
  # A delta over a row the base lacks (#910's, before its pull request lands) is missing, never a delta against zero.
  for id in ab-new cross-new-in-base ratio-gone diff-gone cross-gone-head cross-gone-base value-gone delta-new delta-gone; do
    [ "$(verdict_of $id)" = missing ] || { echo "$id: $(verdict_of $id)"; return 1; }
    [ "$(column_of $id 9)" = 0 ]
  done
  [ "$(verdict_of exists)" = pass ]
  [[ "$output" == *"| cross-new-in-base | #1 | ns/op BenchmarkA-4 head / BenchmarkNew-4 base | 0 | - | - | - | <= 2 | **missing** | gate |"* ]]
  # The same rows as report rules: still missing — said, and failing nothing — never pass.
  fixture_conf \
    "rule ab-new s ab ns/op BenchmarkNew-4 - <= 1.05 #1 report" \
    "rule cross-new-in-base s cross ns/op BenchmarkA-4 BenchmarkNew-4 <= 2 #1 report" \
    "rule value-gone s value allocs/op BenchmarkGone-4 - <= 0 #1 report"
  judge s
  [ "$status" -eq 0 ]
  for id in ab-new cross-new-in-base value-gone; do
    [ "$(verdict_of $id)" = missing ] || { echo "$id: $(verdict_of $id)"; return 1; }
  done
  [[ "$output" == *"| ab-new | #1 | ns/op BenchmarkNew-4 head/base | 0 | - | - | - | <= 1.05 | missing | report |"* ]]
  refute grep -q 'pass' <<<"$output"
}

@test "verdict: the processor model and the vCPU count head the verdict, in verdict.md and in every row of verdict.tsv" {
  fixture_conf "rule r s value allocs/op BenchmarkA-4 - <= 5 #1 report"
  for r in $(seq 1 10); do put head "$r" BenchmarkA-4 100 1; done
  run env BENCH_AB_CPUS=4 BENCH_AB_CPU_MODEL='AMD EPYC 7763 64-Core Processor' bash "$CI_DIR/bench-ab-verdict.sh" s "$WORK/raw" "$WORK/out"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "### bench A/B: s on AMD EPYC 7763 64-Core Processor, 4 vCPUs" ]
  [ "$(head -n 1 "$WORK/out/verdict.md")" = "${lines[0]}" ]
  [ "$(head -n 1 "$WORK/out/verdict.tsv" | awk -F'\t' '{ print $15, $16 }')" = "cpu-model vcpus" ]
  [ "$(column_of r 15)" = "AMD EPYC 7763 64-Core Processor" ]
  [ "$(column_of r 16)" = 4 ]
  # cpuinfo's "model name" when nothing names it, blanks squeezed and the markdown's pipe, a backslash and tabs dropped.
  printf 'processor\t: 0\nmodel name\t:   Intel(R) Xeon(R) |Platinum\\ 8573C   CPU @ 2.30GHz\t\nmodel name\t: second\n' >"$WORK/cpuinfo"
  run env BENCH_AB_CPUS=8 BENCH_AB_CPUINFO="$WORK/cpuinfo" bash "$CI_DIR/bench-ab-verdict.sh" s "$WORK/raw" "$WORK/out"
  [ "${lines[0]}" = "### bench A/B: s on Intel(R) Xeon(R) Platinum 8573C CPU @ 2.30GHz, 8 vCPUs" ]
  # A box that says nothing is named so: the verdict never reads without a machine.
  : >"$WORK/empty"
  run env BENCH_AB_CPUS=2 BENCH_AB_CPUINFO="$WORK/empty" PATH="$WORK/nopath:/usr/bin:/bin" bash "$CI_DIR/bench-ab-verdict.sh" s "$WORK/raw" "$WORK/out"
  [ "$status" -eq 0 ]
  [[ "${lines[0]}" == "### bench A/B: s on "*", 2 vCPUs" ]]
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

@test "build: the module is the nearest go.mod above the package — the package's own (a vendor module), a family's, the root's" {
  build_fixture
  for t in base head; do
    mkdir -p "$WORK/$t/third-party/transform" "$WORK/$t/third-party/db/writer/redis/sub" "$WORK/$t/pkg/v1/data/codec" "$WORK/$t/internal/service/x"
    echo "module v" >"$WORK/$t/third-party/transform/go.mod"
    echo "module r" >"$WORK/$t/third-party/db/writer/redis/go.mod"
    echo "module d" >"$WORK/$t/pkg/v1/data/go.mod"
    echo "module root" >"$WORK/$t/go.mod"
    echo "$t" | tee "$WORK/$t/third-party/transform/a_bench_test.go" "$WORK/$t/third-party/db/writer/redis/sub/a_bench_test.go" \
      "$WORK/$t/pkg/v1/data/codec/a_bench_test.go" "$WORK/$t/internal/service/x/a_bench_test.go" >/dev/null
  done
  # GOWORK=off in the module's directory, the package named relative to it.
  for pair in "third-party/transform:third-party/transform:." "third-party/db/writer/redis/sub:third-party/db/writer/redis:./sub" \
    "pkg/v1/data/codec:pkg/v1/data:./codec" "internal/service/x:.:./internal/service/x"; do
    IFS=: read -r pkg mod rel <<<"$pair"
    : >"$WORK/go.log"
    run bash "$CI_DIR/bench-ab-build.sh" "$WORK/base" "$pkg" "$WORK/b.test" "$WORK/head"
    [ "$status" -eq 0 ] || { echo "$pkg: $output"; return 1; }
    [ "${lines[0]}" = "overlay=all" ]
    dir="$WORK/base/$mod"
    [ "$mod" != . ] || dir="$WORK/base"
    grep -q "^$dir off test -c -o $WORK/b.test $rel\$" "$WORK/go.log" || { echo "$pkg: $(cat "$WORK/go.log")"; return 1; }
  done
  : >"$WORK/go.log"
  run bash "$CI_DIR/bench-ab-build.sh" "$WORK/head" third-party/transform "$WORK/h.test"
  [ "$status" -eq 0 ]
  grep -q "^$WORK/head/third-party/transform off test -c -o $WORK/h.test \.\$" "$WORK/go.log"
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

@test "step0: STEP0_PROFILES runs the binary once a profile over STEP0_PATTERN, keeps each pprof and its top, and never fails" {
  build_fixture
  cat >"$WORK/go" <<'EOF'
#!/usr/bin/env bash
echo "$PWD $*" >>"${STUB_LOG:?}"
if [ "$1 $2" = "tool pprof" ]; then echo "flat%  sum%  cum"; echo "stub top of $6"; else echo "./p/x.go:3:6: can inline f" >&2; fi
EOF
  chmod +x "$WORK/go"
  cat >"$WORK/bin.test" <<EOF
#!/usr/bin/env bash
echo "\$PWD K=\${K:-} \$*" >>"$WORK/runs"
for a in "\$@"; do case "\$a" in -test.*profile=*) echo profile >"\${a#*=}" ;; esac; done
EOF
  chmod +x "$WORK/bin.test"
  # Without STEP0_PROFILES: no run of the binary, no profile (perf stat is absent here too).
  run env PERF="$WORK/no-perf" bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s0/head"
  [ "$status" -eq 0 ]
  [ ! -e "$WORK/runs" ]
  [ ! -e "$WORK/s0/head-mutex.pprof" ]
  # A relative prefix and binary still land where asked: the run is from the package directory.
  cd "$WORK"
  run env PERF="$WORK/no-perf" STEP0_PROFILES=mutex,block,cpu STEP0_PATTERN='^BenchmarkX$' SUITE_PATTERN='^BenchmarkA$' \
    SUITE_BENCHTIME=9x SUITE_CPU=4 SUITE_ENV=K=1 bash "$CI_DIR/bench-ab-step0.sh" head m/p bin.test s1/head
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$WORK/runs")" -eq 3 ]
  grep -q "^$WORK/head/m/p K=1 -test.run=^\$ -test.bench=^BenchmarkX\$ -test.benchtime=9x -test.cpu=4 -test.count=1 -test.timeout=60m -test.mutexprofile=$WORK/s1/head-mutex.pprof$" "$WORK/runs"
  grep -q -- "-test.blockprofile=$WORK/s1/head-block.pprof$" "$WORK/runs"
  grep -q -- "-test.cpuprofile=$WORK/s1/head-cpu.pprof$" "$WORK/runs"
  for kind in mutex block cpu; do
    [ -s "$WORK/s1/head-$kind.pprof" ]
    grep -qF "# go tool pprof -top, the $kind profile of one run over ^BenchmarkX\$" "$WORK/s1/head-$kind-top.txt"
    grep -q "stub top of $WORK/s1/head-$kind.pprof" "$WORK/s1/head-$kind-top.txt"
  done
  grep -q "tool pprof -top -nodecount=40 $WORK/bin.test $WORK/s1/head-cpu.pprof" "$WORK/go.log"
  # The pattern of the suite when STEP0_PATTERN is absent.
  rm -f "$WORK/runs"
  run env PERF="$WORK/no-perf" STEP0_PROFILES=cpu SUITE_PATTERN='^BenchmarkA$' bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s2/head"
  [ "$status" -eq 0 ]
  grep -q -- "-test.bench=^BenchmarkA\$ " "$WORK/runs"
  # A run that fails, a binary that is not there, a name that is no profile.
  printf '#!/bin/sh\nexit 1\n' >"$WORK/bin.test"
  run env PERF="$WORK/no-perf" STEP0_PROFILES=mutex bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s3/head"
  [ "$status" -eq 0 ]
  grep -q "mutex profile: the run failed" "$WORK/s3/head-mutex-top.txt"
  run env PERF="$WORK/no-perf" STEP0_PROFILES=mutex bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/none.test" "$WORK/s4/head"
  [ "$status" -eq 0 ]
  grep -q "no binary" "$WORK/s4/head-mutex-top.txt"
  run env PERF="$WORK/no-perf" STEP0_PROFILES=mutex,heap bash "$CI_DIR/bench-ab-step0.sh" "$WORK/head" m/p "$WORK/bin.test" "$WORK/s5/head"
  [ "$status" -eq 2 ]
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

@test "env: the processor model and the vCPU count are the first two lines of the record" {
  printf 'processor\t: 0\nmodel name\t: AMD EPYC 9V74 80-Core Processor\n' >"$WORK/cpuinfo"
  run env CPUINFO="$WORK/cpuinfo" SUITE_NAME=kcache bash "$CI_DIR/bench-ab-env.sh"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "cpu-model:     AMD EPYC 9V74 80-Core Processor" ]
  [ "${lines[1]}" = "cpus:          $(nproc)" ]
  [ "${lines[2]}" = "suite:         kcache" ]
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

@test "workflow: a pull request keeps one run a ref and cancels the one it replaces; a dispatch has a group of its own, suites and head in it, and is never cancelled" {
  group="$(sed -n 's/^  group: //p' "$WF")"
  [ "$(grep -c '^  group: ' "$WF")" -eq 1 ]
  # The pull request's group is the one it always had: the workflow and the ref, nothing else.
  [[ "$group" == '${{ github.workflow }}-${{ github.ref }}${{ github.event_name == '"'"'workflow_dispatch'"'"' && format('* ]]
  [[ "$group" == *"|| '' }}" ]]
  # The dispatch's adds what makes two dispatches two measurements.
  for input in suites head mode base runner; do
    [[ "$group" == *"inputs.$input"* ]] || { echo "the dispatch's group lacks inputs.$input: $group"; return 1; }
  done
  # An input in the group is a name, never a command: it sits in no `run:` and no `${{ }}` of its own.
  refute grep -q 'group:.*\$\{\{ inputs\.' "$WF"
  grep -q "cancel-in-progress: \${{ github.event_name == 'pull_request' }}" "$WF"
}

@test "workflow: the legs build, interleave, compare, attribute and judge through the lane's scripts, and publish whatever happened" {
  for script in conf tree build env run step0 verdict; do
    grep -q "bash scripts/ci/bench-ab-$script.sh" "$WF" || { echo "no step runs bench-ab-$script.sh"; return 1; }
    [ -f "$CI_DIR/bench-ab-$script.sh" ]
  done
  # Step 0 asks for the profiles the suite's record names, through the environment.
  grep -q 'export STEP0_PROFILES="\$SUITE_STEP0_PROFILES" STEP0_PATTERN="\$SUITE_STEP0_PATTERN"' "$WF"
  grep -q 'GOTOOLCHAIN: local' "$WF"
  grep -q 'base="$OUT/base.txt" head="$OUT/head.txt"' "$WF"
  grep -B1 -A2 'actions/upload-artifact@' "$WF" | grep -q 'if: \${{ !cancelled() }}'
  grep -q 'name: bench-ab-\${{ matrix.suite }}-\${{ github.run_id }}-\${{ github.run_attempt }}' "$WF"
  grep -q 'fail-fast: false' "$WF"
  grep -q 'cancel-in-progress: \${{ github.event_name == '"'"'pull_request'"'"' }}' "$WF"
}
