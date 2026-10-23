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

> **Numbers vary across machines, and these were taken on a busy one, and NOT
> on the CPU the package is written for.** The load average was about 17 on 10
> cores while they ran. Every figure is the median over the runs, in ns/op, and
> the ratios between rows of one table — measured in the same process — are the
> trustworthy part. **There is no row measured on a real x86-64 CPU:** the only
> machine available was an Apple M1 Pro. The AVX2 rows below are the amd64
> binary run through Rosetta 2, which translates each AVX2 instruction to
> NEON: they show that the vector form runs and is correct, and say little
> about its speed on a Skylake or a Zen. **The decision to drop `IndexByte` is
> proven on the M1 only**: under Rosetta the word loop is ahead of the
> translated `bytes.IndexByte` between 8 and 64 bytes, which measures the
> translator's cost for the standard library's SSE2 path, not a win on x86-64.
> It must be taken again on real x86-64 (`go test -bench DroppedIndexByte`,
> built with `GOEXPERIMENT=simd`) before anyone relies on it there.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 (native); amd64 under Rosetta 2 (second table) |
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

## What the figures do not settle

- The amd64 speed of every row, on a CPU that is not Apple's. The SWAR and
  AVX2 rows need a run on the Cascade Lake of the target (no AVX-512 assumed)
  and on a Zen: `GOARCH=amd64 go test -c` and `-test.bench .`, with and
  without `GOEXPERIMENT=simd`.
- `twoScanMin` (64) is the M1's crossover. On x86-64 `bytes.IndexByte` reads
  with SSE2 below 32 bytes and AVX2 above; the crossover may move down.
