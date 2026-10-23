<!-- generated from internal/kernel/swar/swar_bench_test.go and dropped_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -count=8 -benchtime=100ms ./swar/` to refresh (GOEXPERIMENT=simd GOARCH=amd64 for the vector rows); not `make bench`, which regenerates the codec's report only -->
# Benchmarks — `internal/kernel/swar`

First-match byte search a word at a time. The benchmarks exist to answer one
question per function — **does it beat what a caller would write without
it?** — because the package keeps a function only while the answer is yes
(`CLAUDE.md`). The competitors are `bytes.IndexByte`, an assembly loop over 32
bytes at a time; `bytes.IndexAny`; two `bytes.IndexByte` scans; and the byte
loop with its predicate inlined or in a table (the shape of `encoding/json`'s
`safeSet`). Every search runs over slices of 4 to 4096 bytes with the match in
the LAST byte: the scan that reads the most.

## Reproducibility envelope

> **Numbers vary across machines, and these were taken on busy ones.** The
> M1 tables ran at a load average of about 17 on 10 cores; the x86-64 tables
> ran on a 1-vCPU VM of a host loaded by CI, and are **noisy** (see their
> section). Every figure is the median over the runs, in ns/op, and the ratios
> between rows of one table — measured in the same process — are the
> trustworthy part. The AVX2 rows of the Rosetta table are the amd64 binary
> translated to NEON: they show that the vector form runs and is correct, and
> say little about its speed. **A real x86-64 CPU is measured in the last
> results section (AMD EPYC 7351P, Zen 1, AVX2, no AVX-512), one machine and
> not the Cascade Lake the SDK targets.** It confirms the decision to drop
> `IndexByte` and moves `twoScanMin` toward 32 (see "What the figures do not
> settle").

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 (native); amd64 under Rosetta 2 (second table); amd64 on an EPYC 7351P (third and fourth tables) |
| Go toolchain       | go1.27.3 darwin/arm64 |
| Git branch         | `feat/kernel-swar` |
| Git commit         | base `fde96eb7a` (uncommitted when measured) |
| Generated (UTC)    | 2026-10-23 |
| Bench wall-clock   | `-benchtime=100ms -count=8` (M1), `-count=6` (Rosetta) |
| Load average       | about 17 on 10 cores |

## Results — Apple M1 Pro, arm64, native (SWAR forms; no vector form exists here)

`public` is the exported function, as a caller reaches it; `swar` is the word
loop alone; `loop` and `table-loop` are the byte loops. Rows are medians of 8
runs, ns/op, columns the slice length.

```
DroppedIndexByte       L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes                 3.0      2.9      3.8      3.0      4.0      6.0     15.8     77.1
  swar                  5.0      2.6      4.3      5.8      8.5     24.2    100.6    371.4
IndexByte2       L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes.IndexAny       13.7     23.4     14.9     20.5     31.0    113.5    364.5   1425.5
  two-scan              6.9      7.3      6.6      7.6      9.5     13.5     30.8    162.0
  loop                  3.3      4.6      8.5     16.8     33.1    133.9    511.5   2036.0
  public                6.0      3.6      5.3      6.6     10.4     14.6     31.8    159.6
  swar                  4.9      3.1      4.4      6.1     10.0     36.3    147.1    557.3
IndexLess       L4       L8      L16      L32      L64     L256    L1024    L4096
  loop                  3.3      4.6      7.3     13.5     23.4     98.5    356.7   1379.0
  public                5.0      2.8      3.5      4.7      7.3     23.7     95.8    378.9
  swar                  5.1      2.7      3.4      4.8      7.4     23.6     96.3    383.9
SetIndex       L4       L8      L16      L32      L64     L256    L1024    L4096
  table-loop            3.7      5.1      9.0     16.6     32.2    133.4    511.4   2061.0
  predicate-loop        3.4      5.8     11.8     21.3     42.7    205.8    683.9   2708.5
  public                7.4      4.8      6.4      9.2     16.0     85.1    202.9    779.5
  swar                  6.9      4.5      5.7      8.7     14.7     50.6    202.6    778.6
```

(`SetIndex/public` at 256 bytes, 85.1, is a noisy cell: `swar`, the same loop
without the dispatch, reads 50.6.) Every one of those benchmarks reports **0 B/op and 0 allocs/op** (`-benchmem`);
the design's `allocs: 0` budgets (`gen_perf_test.go`) hold the claim in the
alloc lane.

### Reading it

- **`IndexByte` — dropped.** `bytes.IndexByte` beats the word loop at every
  length measured but 8 bytes (3.8 against 4.3 ns at 16, 6.0 against 24.2 at
  256, 77.1 against 371.4 at 4096); at 8 bytes the word loop is ahead by 10 %,
  inside the noise. A function that does not win is not kept.
- **`IndexByte2` — kept.** 2.3 to 11 times faster than `bytes.IndexAny` at
  every length (6.0 against 13.7 ns at 4, 159.6 against 1425 at 4096). Against two `bytes.IndexByte` scans the word loop wins below 64
  bytes (3.1 against 7.3 ns at 8, 6.1 against 7.6 at 32) and loses past it (36.3
  against 13.5 at 256), so past 64 bytes `IndexByte2` IS two scans
  (`twoScanMin`): `public` is within a nanosecond of the better of the two at
  every length. Against the
  byte loop it wins from 8 bytes (3.6 against 4.6) to 9 times at 256, and loses
  below 8 (6.0 against 3.3 at 4): a word that is not there cannot be read.
- **`IndexLess` — kept.** No standard-library competitor. The byte loop is 1.6
  times slower at 8 bytes, 4.2 times at 256 and 3.6 times at 4096 (378.9
  against 1379 ns); below 8 bytes it is ahead.
- **`Set.Index` — kept.** The table loop is 2 times slower at 64 bytes (16.0
  against 32.2) and 2.5 to 2.6 times at 1024 and 4096; at 8 bytes the two tie
  (4.8 against 5.1), and below it the table is 2 times ahead (7.4 against 3.7 at
  4) — the dispatch, the copy of the `Set` and the bound's addend are paid
  before a byte is read, a cost a four-byte string does not repay. A JSON string is
  rarely under 8 bytes and its escape scan is the loop this replaces.

## Results — amd64 under Rosetta 2, `GOEXPERIMENT=simd` (the AVX2 rows are translated)

The same benchmarks, built `GOARCH=amd64 GOEXPERIMENT=simd`, run through
Rosetta on the M1; medians of 6 runs. `vector` is the AVX2 form, which
`public` takes from 32 bytes up. **Not a measurement of an x86-64 CPU** (see
the envelope); one cell (`IndexByte2/swar` at 4096) is a translation outlier.

```
DroppedIndexByte       L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes                 7.4      7.4      7.7     10.6     15.7     27.8     53.3    161.2
  swar                 12.1      4.9      6.4      7.2     11.5     38.8    155.3    418.5
  vector                  -        -        -     10.1     15.2     39.0    112.5    446.4
IndexByte2       L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes.IndexAny       27.3     47.5     19.9     22.3     34.2    116.1    403.9   1711.0
  two-scan             15.0     15.2     15.4     18.7     29.6     46.2    105.2    352.9
  loop                  4.7      7.8     15.4     27.2     52.4    208.4    808.1   3280.0
  public                9.9      7.8      8.3      9.7     12.3     26.3     86.7    437.0
  swar                  7.4      5.4      6.1      8.3     12.9     39.8    156.4   2515.0
  vector                  -        -        -      9.0     17.2     25.0     85.3    652.9
IndexLess       L4       L8      L16      L32      L64     L256    L1024    L4096
  loop                  4.3      5.6      9.0     16.5     31.8    127.5    470.6   1886.5
  public                8.4      7.3      7.6     10.6     10.7     24.1     82.1    315.0
  swar                  5.8      5.7      5.6      7.3     10.2     27.7    124.4    398.5
  vector                  -        -        -      8.2      9.8     22.6     76.7    311.5
SetIndex       L4       L8      L16      L32      L64     L256    L1024    L4096
  table-loop            4.0      6.9     12.3     23.2     44.0    211.8    697.7   2817.0
  predicate-loop        7.0     10.4     18.9     37.4     72.6    291.6   1151.0   4595.5
  public               12.3      8.7      9.9     19.4     21.7     53.5    165.5    697.8
  swar                 10.6      6.3     10.4     12.1     18.6     59.8    235.3   1076.5
  vector                  -        -        -     17.0     18.6     45.8    165.0    654.5
```

What it supports, and no more: the AVX2 forms run and answer like the oracle;
the word loop is ahead of the translated `bytes.IndexByte` from 8 to 64 bytes
(4.9 against 7.4 ns at 8, 11.5 against 15.7 at 64) and behind from 256 (38.8
against 27.8) — the reverse of the M1 below 64 bytes, for the reason given in
the envelope;
they read 32 bytes a register; the dropped one-byte AVX2 form takes twice
`bytes.IndexByte`'s time at 1024 bytes (112.5 against 53.3 ns) although the
standard library's assembly is translated too, so it does not beat it; and for
`IndexLess` and `Set.Index` the AVX2 form matches the SWAR form at 64 bytes and
beats it from 256 (22.6 against 27.7 ns and 45.8 against 59.8), while at 32
bytes it is slower (8.2 against 7.3 and 17.0 against 12.1). The production build does NOT set the experiment (the
submission builds without `GOEXPERIMENT`), so the SWAR rows are the ones that
ship on amd64, and they were measured here only through a translator.

## Results — AMD EPYC 7351P (Zen 1), amd64, native — **noisy, one machine**

A real x86-64 CPU, added by hand after the sections above (the header of this
file says why `make bench` does not write it). Machine: a virtual machine with
1 vCPU of an AMD EPYC 7351P 16-Core Processor (Zen 1: AVX2 and SHA-NI, no
AVX-512), Debian 13, Linux 6.12, on a host that was running CI (load about
26 on its 32 threads while these ran; the guest's own load average read 0.4 to
2). Binaries built on the M1 from commit `a7f487ba3` with `GOOS=linux
GOARCH=amd64 go test -c`, once plain and once with `GOEXPERIMENT=simd`, and
run there with `-test.bench=. -test.benchmem -test.count=3` at the default
benchtime. **Medians of 3 runs, not 8, on a shared core**: single cells move by
10 to 100 % between runs, and the two tables below were taken minutes apart
under different host load (the same `bytes.IndexByte` at 4 bytes reads 15.6
plain and 7.8 simd, and the byte loop in `IndexLess` 6.9 against 5.2). Read the
ratios within one table, and the order of the rows, not the nanoseconds. The
same binaries pass `TestVectorFormsRunHere` (it logs `AVX2 vector forms
exercised: true`), `TestEveryValueAtEveryPosition`, `TestAdjacentPairs` and
`TestNothingPastTheLength` on this CPU. 0 B/op and 0 allocs/op everywhere.

Plain build — **what ships** (the submission builds without the experiment, so
`public` is the SWAR form and, past `twoScanMin`, two `bytes.IndexByte`
scans):

```
DroppedIndexByte         L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes                15.6     20.7     19.8     22.3     31.6     53.2    103.6    307.7
  swar                 15.2     14.5     22.2     33.1     49.9    150.7    433.9   1839.0
IndexByte2               L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes.IndexAny       56.5    134.5     97.9    104.6    218.8    567.8   1818.0   3958.0
  two-scan             34.0     30.1     40.6     39.5     58.7     79.2    220.7    983.7
  loop                 10.4     17.6     38.7     33.3    140.2    481.4   1207.0   7368.0
  public               17.6     26.9     31.8     38.6     71.6    112.2    282.6    811.9
  swar                 16.0     20.3     27.9     38.6     64.5    251.2    813.4   3124.0
IndexLess                L4       L8      L16      L32      L64     L256    L1024    L4096
  loop                  6.9     15.0     22.6     69.8    129.3    245.8   1325.0   2969.0
  public               17.7     13.6     15.6     23.5     33.6    110.4    375.1   1945.0
  swar                 19.8     13.5     15.8     30.1     29.1    121.8    484.0   1920.0
SetIndex                 L4       L8      L16      L32      L64     L256    L1024    L4096
  table-loop           14.4     15.5     40.2     51.5    163.0    530.2   1519.0   5808.0
  predicate-loop       10.5     19.1     43.5     75.5    191.2    809.3   2356.0   3689.0
  public               31.6     36.0     46.7     72.9    107.6    359.5   1031.0   2452.0
  swar                 29.7     29.1     42.3     65.2     71.7    278.7   1402.0   2924.0
```

`GOEXPERIMENT=simd` (`vector` is the AVX2 form, which `public` takes from 32
bytes up; the quieter of the two runs):

```
DroppedIndexByte         L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes                 7.8      7.5      8.8      9.4     12.8     16.5     46.0    208.6
  swar                 10.0      6.2      8.6     10.6     15.6     56.3    203.2   1102.0
  vector                  -        -        -     10.7     14.0     38.0    160.0    745.2
IndexByte2               L4       L8      L16      L32      L64     L256    L1024    L4096
  bytes.IndexAny       28.2     71.3     38.4     41.0     83.7    177.7    572.3   2386.0
  two-scan             16.6     15.2     13.2     17.7     19.4     32.9     94.1    329.7
  loop                  6.5     11.0     18.8     30.5     67.5    209.8    903.4   4047.0
  public               10.6      9.3     11.5      9.1     11.3     20.0     77.4    256.0
  swar                  7.3      7.3     13.2     21.7     35.9     97.6    344.6   1451.0
  vector                  -        -        -      7.9      9.7     23.1     76.5    234.9
IndexLess                L4       L8      L16      L32      L64     L256    L1024    L4096
  loop                  5.2      7.0     12.0     18.5     46.5    115.1    396.5   1569.0
  public               11.3      8.2     10.2      9.0     11.3     22.1     77.7    245.6
  swar                  9.2      6.0      7.2      9.6     14.9     47.8    187.7    697.7
  vector                  -        -        -      7.3      9.7     20.8     76.4    256.9
SetIndex                 L4       L8      L16      L32      L64     L256    L1024    L4096
  table-loop            5.8      9.0     16.5     27.1     78.1    231.3    762.6   3020.0
  predicate-loop        5.7      9.4     16.8     27.2     69.0    239.5    790.4   3144.0
  public               13.4     13.5     20.6     13.5     16.9     36.5    103.0    348.6
  swar                 10.5      9.8     13.5     20.1     34.3    115.5    459.4   1735.0
  vector                  -        -        -      9.9     12.2     27.2    105.7    335.5
```

### Reading it (x86-64, Zen 1)

- **`IndexByte` — the drop stands.** `bytes.IndexByte` beats the word loop at
  every length from 32 bytes up in both builds, and at 16 (plain: 19.8 against
  22.2 ns; simd: a tie, 8.8 against 8.6; plain 53.2 against 150.7 at 256, 307.7 against 1839 at 4096; simd 16.5 against 56.3 at 256, 208.6 against 1102 at 4096). The word loop
  is ahead only at 8 bytes (plain 14.5 against 20.7, simd 6.2 against 7.5), a
  few nanoseconds, and the one-byte AVX2 form loses to `bytes.IndexByte` at
  every length (14.0 against 12.8 at 64, 745.2 against 208.6 at 4096). Same
  answer as the M1, on a CPU where the standard library's SSE2/AVX2 assembly is
  not translated. Nothing here brings the function back.
- **`IndexByte2` — kept, and the vector form is what it is for.** 7 to 10 times
  faster than `bytes.IndexAny` from 64 bytes (simd `public` 11.3 against 83.7
  at 64, 256.0 against 2386 at 4096). With AVX2 `public` is 1.3 to 1.9 times ahead
  of the two scans from 32 bytes (9.1 against 17.7 at 32; 20.0 against 32.9 at
  256).
- **`IndexLess` — kept.** The byte loop is 1.7 to 2.4 times slower from 16 bytes in
  the simd run (12.0 against 7.2 at 16, 115.1 against 47.8 at 256) and ahead at
  4 bytes (5.2 against 9.2) and tied at 8; the plain build shows the same order
  (loop 129.3 against 29.1 at 64) with noisier cells. AVX2 beats the word loop
  from 32 bytes (7.3 against 9.6; 20.8 against 47.8 at 256), unlike under
  Rosetta where it was slower at 32.
- **`Set.Index` — kept, with a smaller margin than the M1's in the plain build.**
  Against the table loop `public` is ahead from 64 bytes: 1.5 times at 64 and
  256 (107.6 against 163.0; 359.5 against 530.2) and 2.4 at 4096 (2452 against
  5808), against the 2 to 2.6 times of the M1. Below 64 the table is ahead or
  tied in the plain build (51.5 against 72.9 at 32, 15.5 against 36.0 at 8): the
  dispatch and the bound's addend cost more here than on the M1. With AVX2 the
  search is 2 to 9 times ahead from 32 bytes (13.5 against 27.1 at 32, 348.6
  against 3020 at 4096). A JSON string scan rarely sits under 64 bytes of
  plain text, but this is the row to take again on a quiet machine.

## What the figures do not settle

- The amd64 speed on the CPU the SDK targets. One real x86-64 machine is
  measured above (a Zen 1 virtual core, on a loaded host, 3 runs per cell). The
  Cascade Lake of the target (no AVX-512 assumed) and a quiet host are still to
  do: `GOARCH=amd64 go test -c` and `-test.bench .`, with and without
  `GOEXPERIMENT=simd`, `-count=8`.
- `twoScanMin` (64) is the M1's crossover and **not clearly the x86-64 one**. On
  the EPYC, `IndexByte2/swar` against `two-scan`: plain build 27.9 against 40.6
  ns at 16 bytes, 38.6 against 39.5 at 32 (a tie), 64.5 against 58.7 at 64; simd
  build 13.2 against 13.2 at 16, 21.7 against 17.7 at 32. The word loop stops
  winning between 16 and 32 bytes there, and past 32 the two scans are ahead or
  tied, so the crossover on this machine is nearer 32 than 64. The effect is a
  few nanoseconds in the 32 to 63 byte band, inside this run's noise, and with
  `GOEXPERIMENT=simd` the AVX2 form takes over from 32 anyway. The constant is
  **left at 64**: lowering it to 32 is a candidate for a quiet-host, 8-run
  measurement, not a change these numbers justify.
