<!-- generated from internal/service/transform/transform_bench_test.go — run `cd internal/service && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s -count=3 ./transform/` to refresh -->
# Benchmarks — `internal/service/transform`

The three stdlib schemes (gzip, raw DEFLATE, zlib) had never been measured. This
report exists to answer four questions a caller actually has, and one the
maintainer had:

> **What does a compress cost, what makes that number move, what does the
> decompression bound cost, and what did recycling the stdlib codecs buy?**

The short answer to the last one, which is why `pool.go` exists: at 256 bytes,
**one gzip `Compress` went from 493 060 ns and 1 076 250 B/op to 10 175 ns and
258 B/op** — 48.5× faster and 4 172× fewer bytes. Nothing about the compression
changed: the two builds were run side by side over all 30 scheme x corpus x size
combinations and every SHA-256 matched, so the wire output is **byte-identical**.
What changed is that the stdlib encoder is no longer built and thrown away on
every call.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. This is a
> memory-ballooned VM: 15 GiB nominal with a balloon target of 8 GiB, so the
> host can reclaim half of it mid-run. The *shape* of every result below is what
> travels — the ratios, the cliffs and the orderings — not the nanoseconds.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 15 GiB (ballooned VM) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `3805069` (pre-commit; `pool.go` in the working tree) |
| Generated (UTC)    | 2026-09-10 |
| Machine load       | `16:08:39 up 18 days, 3:51, 2 users, load average: 0.22, 0.60, 0.56` at start; 0.54/0.55/0.56 at the first run, no other job on the box |
| Bench wall-clock   | `-benchtime=1s -count=3`; 118 rows ≈ 9 min (current code), 81 rows ≈ 6 min (pre-pooling build) |

### Method

- **Every published number is the median of at least three runs.** The matrix is
  `-count=3`; rows that disagreed with their neighbours arithmetically were
  re-run at `-count=5` and are called out in §10. Each row's run-to-run spread
  `(max−min)/median` was computed and used as the noise floor when deciding
  whether a delta is real.
- **Before/after is a real before, not a stash.** The pre-pooling package
  (`git show HEAD:internal/service/transform/*.go`) was copied into a scratch
  module under `/dev/shm` whose module path is `github.com/kitsunium/sdk/...`,
  so it imports the *same* `internal/core` and `internal/kernel` through
  `replace` and compiles against the same toolchain. Both binaries run the same
  benchmark bodies.
- **Everything ran out of `/dev/shm`**, never `/tmp`: both test binaries, both
  profiles and every output file. Nothing here touches a real filesystem.
- **`-memprofilerate=1` distorts CPU by 2.3×** (23 006 ns/op against 10 037 for
  the same row) and is never mixed with a CPU profile in the same run. The CPU
  and allocation profiles below are separate runs of the same benchmark.
- **The two builds were checked for output equality before any of their timings
  were compared.** A performance change that alters the bytes on the wire is not
  a performance change; both binaries were driven over `{gzip,flate,zlib}` x
  `{random,text}` x `{64, 256, 4 KiB, 64 KiB, 1 MiB}` and all 30 SHA-256 digests
  are equal.

## 1. What pooling bought

### 1.1 The profile that motivated it, verbatim

`BenchmarkCompress/gzip/text/256`, `-benchtime=5s`, pre-pooling binary:

```
Duration: 10.58s, Total samples = 19760ms (186.73%)
Showing nodes accounting for 11660ms, 59.01% of 19760ms total
      flat  flat%   sum%        cum   cum%
    3720ms 18.83% 18.83%     3720ms 18.83%  runtime.memclrNoHeapPointers
    2540ms 12.85% 31.68%     4610ms 23.33%  compress/flate.(*fastEncL6).encode
    1570ms  7.95% 39.63%     1570ms  7.95%  runtime.madvise
    1200ms  6.07% 45.70%     1200ms  6.07%  runtime.futex
    1100ms  5.57% 51.27%     1460ms  7.39%  runtime.typePointers.next
     570ms  2.88% 54.15%     2370ms 11.99%  runtime.scanObject
     510ms  2.58% 56.73%      950ms  4.81%  runtime.scanblock
     450ms  2.28% 59.01%      450ms  2.28%  runtime.procyieldAsm
```

**`runtime.memclrNoHeapPointers` at 18.83 %** — the figure `pool.go`'s package
comment quotes as 18.7 % is reproduced here to within a rounding of the sample
grid. It is the Go allocator zeroing the encoder's hash tables, which
`fastEncL6.encode` then overwrites. `Total samples = 186.73 %` of wall time is
the second half of the story: nearly a full extra core is GC, on a benchmark
that compresses 256 bytes.

The same benchmark on the pooled code:

```
Duration: 6.09s, Total samples = 6150ms (100.95%)
Showing nodes accounting for 5050ms, 82.11% of 6150ms total
      flat  flat%   sum%        cum   cum%
    1680ms 27.32% 27.32%     1680ms 27.32%  compress/flate.(*huffmanEncoder).bitCounts
     860ms 13.98% 41.30%     1150ms 18.70%  compress/flate.(*fastEncL6).encode
     530ms  8.62% 49.92%      530ms  8.62%  compress/flate.(*huffmanBitWriter).generateCodegen
     440ms  7.15% 57.07%      480ms  7.80%  slices.insertionSortCmpFunc[go.shape.struct { compress/flate.literal uint16; compress/flate.freq uint16 }]
     370ms  6.02% 63.09%      370ms  6.02%  compress/flate.(*huffmanEncoder).bitLength
     230ms  3.74% 66.83%      290ms  4.72%  compress/flate.(*huffmanBitWriter).writeDynamicHeader
     210ms  3.41% 70.24%      210ms  3.41%  compress/flate.(*huffmanBitWriter).writeTokens
     210ms  3.41% 73.66%     2880ms 46.83%  compress/flate.(*huffmanEncoder).generate
```

`memclrNoHeapPointers` is gone from the top eight entirely, `Total samples` is
back to **100.95 %** — one core, no GC crew — and every remaining entry is
DEFLATE doing the work the caller asked for. Huffman table *generation* is now
46.83 % cumulative, which is the honest cost of emitting a dynamic block for a
small payload (§4).

### 1.2 The allocation profile, verbatim

Same benchmark, `-memprofilerate=1`, `-sample_index=alloc_space`. Pre-pooling:

```
Showing nodes accounting for 2102309.42kB, 99.94% of 2103524.60kB total
      flat  flat%   sum%        cum   cum%
784392.22kB 37.29% 37.29% 784392.22kB 37.29%  compress/flate.newFastEnc (inline)
640321.64kB 30.44% 67.73% 640321.64kB 30.44%  compress/flate.(*fastGen).addBlock
  528264kB 25.11% 92.84% 1461982.92kB 69.50%  compress/flate.NewWriter (inline)
128066.08kB  6.09% 98.93% 933718.92kB 44.39%  compress/flate.(*compressor).init
18513.88kB  0.88% 99.81% 18513.88kB  0.88%  compress/flate.newHuffmanEncoder (inline)
 2751.38kB  0.13% 99.94% 21260.62kB  1.01%  compress/flate.newHuffmanBitWriter
    0.23kB 1.1e-05% 99.94% 2103415.72kB   100%  testing.(*B).runN
```

**99.94 % of every byte a 256-byte gzip allocated was `compress/flate` building
its encoder.** Not 99 % as a figure of speech — that is the whole profile; there
is no line for the caller's output buffer because it does not round to 0.06 %.

Pooled:

```
Showing nodes accounting for 49.85MB, 99.07% of 50.32MB total
      flat  flat%   sum%        cum   cum%
   24.41MB 48.52% 48.52%    24.41MB 48.52%  bytes.growSlice
   12.21MB 24.26% 72.78%    36.62MB 72.78%  bytes.(*Buffer).grow
    9.16MB 18.19% 90.97%     9.16MB 18.19%  bytes.NewBuffer
    1.53MB  3.04% 94.01%     1.53MB  3.04%  compress/flate.newFastEnc (inline)
    1.25MB  2.48% 96.50%     1.25MB  2.48%  compress/flate.(*fastGen).addBlock
    1.03MB  2.05% 98.55%     2.85MB  5.67%  compress/flate.NewWriter
    0.25MB   0.5% 99.04%     1.82MB  3.62%  compress/flate.(*compressor).init
```

The profile has inverted: **90.97 % is now the caller's own output buffer** and
`compress/flate.NewWriter` is down to 5.67 % cumulative. That residue is not a
leak — it is the pool genuinely missing, because the runtime empties every
`sync.Pool` at each GC. §7 measures exactly how often that happens and what it
costs, because it is the one number that decides whether this change helps a
given caller.

### 1.3 Before → after, per scheme

Median of 3, `-benchtime=1s`. `text` is the log-line corpus; the size is the
plaintext.

**Compress**

| scheme | size | before ns | after ns | speed-up | before B/op | after B/op | allocs |
|---|---:|---:|---:|---:|---:|---:|---:|
| gzip  | 256 B  | 493 060 | **10 175** | **48.5×** | 1 076 250 | **258** | 17 → 3 |
| flate | 256 B  | 486 828 | **9 876**  | **49.3×** | 1 076 010 | **168** | 15 → 2 |
| zlib  | 256 B  | 509 634 | **10 279** | **49.6×** | 1 076 193 | **258** | 18 → 3 |
| gzip  | 4 KiB  | 482 175 | 18 955 | 25.4× | 1 076 553 | 577 | 18 → 4 |
| flate | 4 KiB  | 539 692 | 17 920 | 30.1× | 1 076 025 | 192 | 15 → 2 |
| zlib  | 4 KiB  | 520 779 | 19 995 | 26.0× | 1 076 208 | 274 | 18 → 3 |
| gzip  | 64 KiB | 523 476 | 156 218 | 3.35× | 1 076 883 | 1 017 | 18 → 4 |
| flate | 64 KiB | 597 873 | 151 630 | 3.94× | 1 076 661 | 816 | 16 → 3 |
| zlib  | 64 KiB | 511 454 | 190 801 | 2.68× | 1 076 824 | 1 057 | 19 → 4 |
| gzip  | 1 MiB  | 2 934 639 | 2 667 901 | 1.10× | 1 084 052 | 10 425 | 21 → 7 |
| flate | 1 MiB  | 2 832 869 | 2 559 492 | 1.11× | 1 083 827 | 10 270 | 19 → 6 |
| zlib  | 1 MiB  | 3 350 109 | 3 051 442 | 1.10× | 1 083 997 | 10 774 | 22 → 7 |

**The speed-up is a function of payload size, and it has to be.** The encoder
costs ~1.076 MB to build no matter what it then compresses, so the smaller the
payload the larger the share that construction was. At 1 MiB the allocation
still falls **104×** but the wall clock moves only 10 %, because at that size
the actual DEFLATE work is 2.6 ms and the construction it replaced was ~0.27 ms.
A caller compressing megabytes gains almost nothing here; a caller compressing
log lines gains fifty-fold.

**Decompress**

| scheme | size | before ns | after ns | speed-up | before B/op | after B/op | allocs |
|---|---:|---:|---:|---:|---:|---:|---:|
| gzip  | 256 B  | 19 935 | **6 581** | **3.03×** | 42 024 | **840** | 9 → 4 |
| flate | 256 B  | 19 153 | **6 233** | **3.07×** | 41 320 | **841** | 8 → 4 |
| zlib  | 256 B  | 20 023 | **6 558** | **3.05×** | 41 404 | **844** | 10 → 5 |
| gzip  | 4 KiB  | 29 469 | 15 141 | 1.95× | 55 720 | 14 548 | 17 → 12 |
| flate | 4 KiB  | 27 799 | 13 984 | 1.99× | 55 016 | 14 550 | 16 → 12 |
| zlib  | 4 KiB  | 31 045 | 16 360 | 1.90× | 55 100 | 14 554 | 18 → 13 |
| gzip  | 64 KiB | 125 733 | 106 335 | 1.18× | 244 905 | 203 863 | 24 → 19 |
| flate | 64 KiB | 116 729 | 101 078 | 1.15× | 244 201 | 203 875 | 23 → 19 |
| zlib  | 64 KiB | 149 717 | 126 321 | 1.19× | 244 286 | 203 862 | 25 → 20 |

The decoder is a **41 KB** object, not a 1 MB one — a 32 KiB window plus the
Huffman decode tables — so the decompression half gains 3× where the compression
half gains 48×, and it gains it for the same reason and in the same place.

At **1 MiB the decompression gain is inside the noise and sometimes negative**
(§10): the pooled decoder is 1.1 % of what such a call allocates, and the row is
dominated by `io.ReadAll` growing a 1 MiB buffer.

## 2. Pool hit vs pool miss — measured inside the current code

The before/after tables above compare two *binaries*. This section compares two
*paths* inside the one that shipped, which is the more useful number for a
caller: a process that calls `Compress` once per request pays the hit; a process
whose GC runs between requests, or that has just started, pays the miss.

The miss is forced by taking the pooled object out immediately before the call,
so the take that follows finds nothing and constructs. Payload is 256 B of
log-line text.

| scheme | verb | hit (ns) | miss (ns) | ratio | hit B/op | miss B/op |
|---|---|---:|---:|---:|---:|---:|
| gzip  | Compress   | **10 186** | 515 292 | **50.6×** | 258 | 1 077 256 |
| flate | Compress   | **9 799**  | 419 069 | **42.8×** | 177 | 1 076 706 |
| zlib  | Compress   | **10 210** | 356 512 | **34.9×** | 249 | 1 076 762 |
| gzip  | Decompress | **6 390**  | 21 539  | **3.37×** | 841 | 42 084 |
| flate | Decompress | **6 029**  | 21 352  | **3.54×** | 842 | 41 385 |
| zlib  | Decompress | **6 342**  | 18 475  | **2.91×** | 844 | 41 455 |

**Measurement overhead is disclosed rather than assumed.** Each miss row carries
one extra `recycler.Pool.Get`; `BenchmarkPoolSteal` prices a Get plus a Put at
**17.5 ns, 0 allocs/op**, so the steal is at most **0.09 %** of the cheapest miss row
(18 475 ns) and does not appear in the hit rows at all. No row here changes sign
or magnitude if you subtract it.

**The two instruments agree.** The gzip miss row (515 292 ns / 1 077 256 B) and
the pre-pooling *binary*'s gzip 256 B row (493 060 ns / 1 076 250 B) were
produced by completely different means — one steals from a live pool, the other
is a separate build with no pool at all — and land within **4.5 % on time and
0.1 % on bytes**. flate and zlib agree on bytes to 0.07 % and on time to 14 %
and 30 % respectively; the time gap there is GC scheduling, which at 1 MB/op is
the dominant term and is not reproducible to better than that. Bytes are the
trustworthy column at this magnitude.

## 3. The full matrix

Median of 3 runs, `-benchtime=1s`. `ratio` is `len(output)/len(input)` measured
on the same run that produced the timing.

```
BenchmarkCompress/gzip/random/64-8                                7313 ns/op           8.75 MB/s         1.3910 ratio              261 B/op           3 allocs/op
BenchmarkCompress/gzip/random/256-8                               2297 ns/op         111.47 MB/s         1.0980 ratio              444 B/op           3 allocs/op
BenchmarkCompress/gzip/random/4096-8                              7429 ns/op         551.32 MB/s         1.0060 ratio             5138 B/op           3 allocs/op
BenchmarkCompress/gzip/random/65536-8                            43438 ns/op        1508.73 MB/s         1.0000 ratio            75149 B/op           3 allocs/op
BenchmarkCompress/gzip/random/1048576-8                        1406571 ns/op         745.48 MB/s         1.0000 ratio          2287615 B/op           8 allocs/op
BenchmarkCompress/gzip/text/64-8                                  4333 ns/op          14.77 MB/s         1.3910 ratio              252 B/op           3 allocs/op
BenchmarkCompress/gzip/text/256-8                                10175 ns/op          25.16 MB/s         0.4883 ratio              258 B/op           3 allocs/op
BenchmarkCompress/gzip/text/4096-8                               18955 ns/op         216.10 MB/s         0.0354 ratio              577 B/op           4 allocs/op
BenchmarkCompress/gzip/text/65536-8                             156218 ns/op         419.52 MB/s         0.0056 ratio             1017 B/op           4 allocs/op
BenchmarkCompress/gzip/text/1048576-8                          2667901 ns/op         393.03 MB/s         0.0036 ratio            10425 B/op           7 allocs/op
BenchmarkCompress/flate/random/64-8                               7441 ns/op           8.60 MB/s         1.1090 ratio              246 B/op           3 allocs/op
BenchmarkCompress/flate/random/256-8                              2199 ns/op         116.39 MB/s         1.0270 ratio              422 B/op           3 allocs/op
BenchmarkCompress/flate/random/4096-8                             7450 ns/op         549.83 MB/s         1.0020 ratio             5128 B/op           3 allocs/op
BenchmarkCompress/flate/random/65536-8                           46801 ns/op        1400.30 MB/s         1.0000 ratio            75391 B/op           3 allocs/op
BenchmarkCompress/flate/random/1048576-8                       1302089 ns/op         805.30 MB/s         1.0000 ratio          2287491 B/op           8 allocs/op
BenchmarkCompress/flate/text/64-8                                 4203 ns/op          15.23 MB/s         1.1090 ratio              251 B/op           3 allocs/op
BenchmarkCompress/flate/text/256-8                                9876 ns/op          25.92 MB/s         0.4180 ratio              168 B/op           2 allocs/op
BenchmarkCompress/flate/text/4096-8                              17920 ns/op         228.57 MB/s         0.0310 ratio              192 B/op           2 allocs/op
BenchmarkCompress/flate/text/65536-8                            151630 ns/op         432.21 MB/s         0.0053 ratio              816 B/op           3 allocs/op
BenchmarkCompress/flate/text/1048576-8                         2559492 ns/op         409.68 MB/s         0.0036 ratio            10270 B/op           6 allocs/op
BenchmarkCompress/zlib/random/64-8                                7567 ns/op           8.46 MB/s         1.2030 ratio              267 B/op           3 allocs/op
BenchmarkCompress/zlib/random/256-8                               2408 ns/op         106.33 MB/s         1.0510 ratio              420 B/op           3 allocs/op
BenchmarkCompress/zlib/random/4096-8                              9492 ns/op         431.50 MB/s         1.0030 ratio             5113 B/op           3 allocs/op
BenchmarkCompress/zlib/random/65536-8                            74484 ns/op         879.87 MB/s         1.0000 ratio            77337 B/op           3 allocs/op
BenchmarkCompress/zlib/random/1048576-8                        1693633 ns/op         619.13 MB/s         1.0000 ratio          2287722 B/op           8 allocs/op
BenchmarkCompress/zlib/text/64-8                                  4420 ns/op          14.48 MB/s         1.2030 ratio              247 B/op           3 allocs/op
BenchmarkCompress/zlib/text/256-8                                10279 ns/op          24.91 MB/s         0.4414 ratio              258 B/op           3 allocs/op
BenchmarkCompress/zlib/text/4096-8                               19995 ns/op         204.85 MB/s         0.0325 ratio              274 B/op           3 allocs/op
BenchmarkCompress/zlib/text/65536-8                             190801 ns/op         343.48 MB/s         0.0054 ratio             1057 B/op           4 allocs/op
BenchmarkCompress/zlib/text/1048576-8                          3051442 ns/op         343.63 MB/s         0.0036 ratio            10774 B/op           7 allocs/op

BenchmarkDecompress/gzip/random/64-8                              1174 ns/op          54.52 MB/s               649 B/op           4 allocs/op
BenchmarkDecompress/gzip/random/256-8                             1247 ns/op         205.26 MB/s               841 B/op           4 allocs/op
BenchmarkDecompress/gzip/random/4096-8                            9057 ns/op         452.26 MB/s             14557 B/op          12 allocs/op
BenchmarkDecompress/gzip/random/65536-8                          94940 ns/op         690.29 MB/s            203903 B/op          19 allocs/op
BenchmarkDecompress/gzip/random/1048576-8                      1599113 ns/op         655.72 MB/s           3278608 B/op          29 allocs/op
BenchmarkDecompress/gzip/text/64-8                                1178 ns/op          54.33 MB/s               649 B/op           4 allocs/op
BenchmarkDecompress/gzip/text/256-8                               6581 ns/op          38.90 MB/s               840 B/op           4 allocs/op
BenchmarkDecompress/gzip/text/4096-8                             15141 ns/op         270.53 MB/s             14548 B/op          12 allocs/op
BenchmarkDecompress/gzip/text/65536-8                           106335 ns/op         616.32 MB/s            203863 B/op          19 allocs/op
BenchmarkDecompress/gzip/text/1048576-8                        2363878 ns/op         443.58 MB/s           3281073 B/op          29 allocs/op
BenchmarkDecompress/flate/random/64-8                             1025 ns/op          62.42 MB/s               649 B/op           4 allocs/op
BenchmarkDecompress/flate/random/256-8                            1096 ns/op         233.51 MB/s               841 B/op           4 allocs/op
BenchmarkDecompress/flate/random/4096-8                           8501 ns/op         481.85 MB/s             14553 B/op          12 allocs/op
BenchmarkDecompress/flate/random/65536-8                         95831 ns/op         683.87 MB/s            203905 B/op          19 allocs/op
BenchmarkDecompress/flate/random/1048576-8                     1577843 ns/op         664.56 MB/s           3279052 B/op          29 allocs/op
BenchmarkDecompress/flate/text/64-8                               1010 ns/op          63.37 MB/s               649 B/op           4 allocs/op
BenchmarkDecompress/flate/text/256-8                              6233 ns/op          41.07 MB/s               841 B/op           4 allocs/op
BenchmarkDecompress/flate/text/4096-8                            13984 ns/op         292.91 MB/s             14550 B/op          12 allocs/op
BenchmarkDecompress/flate/text/65536-8                          101078 ns/op         648.37 MB/s            203875 B/op          19 allocs/op
BenchmarkDecompress/flate/text/1048576-8                       2237868 ns/op         468.56 MB/s           3280679 B/op          29 allocs/op
BenchmarkDecompress/zlib/random/64-8                              1214 ns/op          52.71 MB/s               653 B/op           5 allocs/op
BenchmarkDecompress/zlib/random/256-8                             1385 ns/op         184.83 MB/s               845 B/op           5 allocs/op
BenchmarkDecompress/zlib/random/4096-8                           10216 ns/op         400.96 MB/s             14554 B/op          13 allocs/op
BenchmarkDecompress/zlib/random/65536-8                         113381 ns/op         578.01 MB/s            203862 B/op          20 allocs/op
BenchmarkDecompress/zlib/random/1048576-8                      2022908 ns/op         518.35 MB/s           3280157 B/op          29 allocs/op
BenchmarkDecompress/zlib/text/64-8                                1210 ns/op          52.91 MB/s               653 B/op           5 allocs/op
BenchmarkDecompress/zlib/text/256-8                               6558 ns/op          39.04 MB/s               844 B/op           5 allocs/op
BenchmarkDecompress/zlib/text/4096-8                             16360 ns/op         250.37 MB/s             14554 B/op          13 allocs/op
BenchmarkDecompress/zlib/text/65536-8                           126321 ns/op         518.81 MB/s            203862 B/op          20 allocs/op
BenchmarkDecompress/zlib/text/1048576-8                        2672850 ns/op         392.31 MB/s           3280351 B/op          30 allocs/op

BenchmarkCompressReusedDst/gzip/random-8                          5377 ns/op         761.70 MB/s                52 B/op           1 allocs/op
BenchmarkCompressReusedDst/gzip/text-8                           18193 ns/op         225.14 MB/s                64 B/op           1 allocs/op
BenchmarkCompressReusedDst/flate/random-8                         4910 ns/op         834.17 MB/s                52 B/op           1 allocs/op
BenchmarkCompressReusedDst/flate/text-8                          17487 ns/op         234.23 MB/s                63 B/op           1 allocs/op
BenchmarkCompressReusedDst/zlib/random-8                          6814 ns/op         601.12 MB/s                54 B/op           1 allocs/op
BenchmarkCompressReusedDst/zlib/text-8                           19514 ns/op         209.90 MB/s                65 B/op           1 allocs/op
```

## 4. The 128-byte cliff — why 64 B costs 3× what 256 B costs

The matrix contains a row pair that looks like a broken measurement:

```
BenchmarkCompress/gzip/random/64-8                                7313 ns/op           8.75 MB/s         1.3910 ratio              261 B/op           3 allocs/op
BenchmarkCompress/gzip/random/256-8                               2297 ns/op         111.47 MB/s         1.0980 ratio              444 B/op           3 allocs/op
```

**A 64-byte payload costs 3.2× a 256-byte one.** That is exactly the shape of
arithmetic contradiction that means "throw the row away" — so it was
investigated rather than published, and it turns out to be real, reproducible,
and a property of `compress/flate` that a caller should know about.

`(*compressor).storeFast` has three regimes, and the boundaries are hard-coded
byte counts, not properties of the data:

| input | what the stdlib emits | consequence |
|---|---|---|
| ≤ 32 B | a **stored** block | trivially cheap, output is input + framing |
| 33 – 127 B | a **Huffman-only** block | builds a Huffman tree, never looks for a match |
| ≥ 128 B | the normal fast-encode path | matches found, then stored *or* dynamic Huffman, whichever is smaller |

Driven directly against the stdlib (`gzip.Writer` with `Reset`, warm, 200 000
iterations per point) the cliff is unmistakable:

| size | random ns | random ratio | text ns | text ratio |
|---:|---:|---:|---:|---:|
| 16 | 225 | 2.5625 | 220 | 2.5625 |
| 32 | 225 | 1.7812 | 226 | 1.7812 |
| **33** | **3 592** | 1.7576 | **2 603** | 1.7576 |
| 64 | 6 862 | 1.3906 | 3 796 | 1.3906 |
| 96 | 9 777 | 1.2604 | 4 295 | 1.2604 |
| **127** | **12 224** | 1.1969 | **4 959** | 1.1969 |
| **128** | **1 129** | 1.1953 | **9 310** | 0.9688 |
| 129 | 1 191 | 1.1938 | 9 181 | 0.9612 |
| 256 | 1 682 | 1.0977 | 9 412 | 0.4883 |

Three things a caller should take from this table:

1. **One byte of input changes the cost by 10.8×** — 127 B of incompressible
   data costs 12 224 ns, 128 B costs 1 129 ns. Below 128 the encoder Huffman-codes
   the literals it was handed; at 128 it starts looking for matches, finds none,
   and emits a *stored* block, which is nearly free.
2. **Below 128 bytes the output is always larger than the input, on every
   corpus.** The ratio column at 33–127 B is identical for random and text
   because no match is ever sought: repetition cannot help. Compressing a
   sub-128-byte payload is a pure loss, and it is a loss that costs 3–12 µs.
3. **Below 128 bytes the cost tracks symbol diversity, not compressibility.**
   Random is 1.8× text at 64 B because a flatter literal distribution makes a
   bigger Huffman tree. Above 128 that inverts: random becomes the *cheap* case
   (it stores) and text the expensive one (it encodes).

The 64 B rows in §3 are therefore correct and the inversion is not noise. The
practical rule: **do not compress payloads below ~128 bytes** — this package
will happily make them bigger and charge microseconds for it.

## 5. Compressibility is the largest axis after size

The two corpora are the same lengths and behave nothing alike.

| size | gzip random ns | gzip text ns | random ratio | text ratio |
|---:|---:|---:|---:|---:|
| 4 KiB | 7 429 | 18 955 | 1.0060 | 0.0354 |
| 64 KiB | 43 438 | 156 218 | 1.0000 | 0.0056 |
| 1 MiB | 1 406 571 | 2 667 901 | 1.0000 | 0.0036 |

Above the 128-byte cliff, **incompressible input is 1.9–3.6× cheaper** than
compressible input, because DEFLATE gives up and stores it. It is also the case
where the output is *larger* than the input (`ratio ≥ 1.0`) and where the
allocation is worst: `gzip/random/65536` allocates 75 149 B/op against
`gzip/text/65536`'s 1 017 B/op, all of it the output buffer growing to hold a
payload that got no smaller. A report that benchmarked only the log-line corpus
would have shown this package as 74× more allocation-efficient than it is on
the input a caller most wants to avoid handing it.

**The one lever a caller has** is the append-to-`dst` convention. Passing a
buffer with capacity instead of `nil`, at 4 KiB:

| scheme / corpus | `Compress(nil, …)` | `Compress(scratch[:0], …)` | time | bytes |
|---|---:|---:|---:|---:|
| gzip / random | 7 429 ns / 5 138 B / 3 | 5 377 ns / **52 B** / **1** | −27.6 % | −99.0 % |
| flate / random | 7 450 ns / 5 128 B / 3 | 4 910 ns / **52 B** / **1** | −34.1 % | −99.0 % |
| zlib / random | 9 492 ns / 5 113 B / 3 | 6 814 ns / **54 B** / **1** | −28.2 % | −98.9 % |
| gzip / text | 18 955 ns / 577 B / 4 | 18 193 ns / 64 B / 1 | −4.0 % | −88.9 % |
| flate / text | 17 920 ns / 192 B / 2 | 17 487 ns / 63 B / 1 | −2.4 % | −67.2 % |
| zlib / text | 19 995 ns / 274 B / 3 | 19 514 ns / 65 B / 1 | −2.4 % | −76.3 % |

The remaining 52–65 B and one allocation is the `bytes.Buffer` header escaping;
the payload path is allocation-free. The lever is worth 28–34 % on incompressible
data — where the output buffer must grow to the full payload size — and 2–4 % on
data that compresses, where there was little to grow.

## 6. The zlib level knob — and the ranking pooling corrected

64 KiB payload, `NewZlibCompressor(level)`.

| level | random ns | random ratio | text ns | text ratio |
|---|---:|---:|---:|---:|
| `HuffmanOnly` (−2) | 124 560 | 1.0000 | 229 796 | **0.5781** |
| `BestSpeed` (1) | **63 952** | 1.0000 | **57 748** | 0.0054 |
| `DefaultCompression` (−1) | 75 609 | 1.0000 | 194 542 | 0.0054 |
| level 6 | 75 772 | 1.0000 | 191 598 | 0.0054 |
| `BestCompression` (9) | 922 987 | 1.0000 | 382 618 | 0.0053 |
| `NoCompression` (0), *clamped* | 73 607 | 1.0000 | 195 715 | 0.0054 |

Read it in this order:

- **`BestSpeed` is 3.37× faster than the default on this corpus and gives the
  same ratio to four significant figures.** The default is not the fast choice
  and the corpus decides whether it is the right one.
- **`BestCompression` costs 12.2× the default on incompressible data and buys
  nothing** (`ratio` 1.0000 either way) — it is the exhaustive match search
  finding no matches. On text it costs 2.0× the default for a 1.9 % better ratio.
- **`NoCompression` measures identically to `DefaultCompression`**, on both
  corpora, on time and on ratio. That is `usableZlibLevel`'s ADR 0031 clamp
  showing up as a measurement rather than as a claim.
- **`HuffmanOnly` is not a fast mode on repetitive data.** It is 3.98× `BestSpeed`
  and its ratio is **107× worse** (0.5781 vs 0.0054), because it Huffman-codes
  every one of 65 536 literal bytes instead of letting the match finder delete
  them first.

That last row is the one to dwell on, because **the pre-pooling numbers said the
opposite**:

| level | before (random) | after (random) | before (text) | after (text) |
|---|---:|---:|---:|---:|
| `HuffmanOnly` | **211 373** | 124 560 | **317 687** | 229 796 |
| `BestSpeed` | 406 751 | **63 952** | 385 940 | **57 748** |
| `DefaultCompression` | 581 464 | 75 609 | 540 694 | 194 542 |

Before pooling, `HuffmanOnly` was the fastest level on both corpora — by 1.9× on
random and 1.2× on text.
Not because it compresses faster, but because its *writer is cheaper to build*:
437 079 B/op against `BestSpeed`'s 887 642 B/op, since it needs a 32 KiB window
and no hash tables. Construction dominated so completely that the level sweep
was measuring the constructor and calling it the level. **Removing a fixed cost
did not just make the numbers smaller; it reversed their order.** A level chosen
from the old table would have been chosen for the wrong reason.

## 7. The round trip does not add up — and the reason is `sync.Pool`

A round trip must cost its two halves. At 4 KiB it does not:

| | Compress | Decompress | sum | RoundTrip | gap |
|---|---:|---:|---:|---:|---:|
| gzip / random | 7 380 ns | 7 871 ns | 15 251 | **22 442** | **+47 %** |
| gzip / random B/op | 5 118 | 14 552 | 19 670 | **26 155** | **+6 485 B** |

Same allocation *count* (3 + 12 = 15), 6 485 more *bytes*. An extra 6 485 B/op
with no extra allocation means one allocation is occasionally much larger — and
`6 485 / 1 076 998` (the bytes a cold gzip `Compress` costs over a warm one) is
**0.6 %**. The hypothesis: the round trip allocates 26 KB per iteration, which
triggers GC more often, and **the runtime empties every `sync.Pool` at each GC
cycle** — so 0.6 % of round-trip iterations rebuild the 1 MiB encoder.

Tested by pinning the iteration count and disabling the collector, so the pool is
never emptied (`-benchtime=5000x -count=3`):

| | `GOGC` default | `GOGC=off` |
|---|---:|---:|
| Compress B/op | 5 409 | 5 191 |
| Decompress B/op | 14 567 | 14 536 |
| **sum** | 19 976 | **19 727** |
| **RoundTrip B/op** | **26 258** | **19 735** |
| gap | +6 282 B | **+8 B** |
| RoundTrip ns | 23 248 | 13 976 |

With no GC the arithmetic closes to **8 bytes out of 19 735**, and the round trip
drops 40 % in time. The gap was never a defect in the benchmark; it is the
pooling change's real boundary condition, and it is the thing to say out loud:

> **This pool's benefit is a function of the caller's GC rate, not only of its
> call rate.** A service that allocates heavily between compressions will find
> the pool empty and pay the miss column of §2. The pool converts a guaranteed
> per-call cost into a probabilistic one; it does not remove it.

## 8. What the decompression bound costs

`readAllBounded` is one `io.LimitReader` plus one length comparison against
`maxDecompressedBytes`. Against a bare `io.ReadAll` over the same bytes
(`-count=5`, because the 1 MiB pair needed it — §10):

| plaintext | bounded ns | unbounded ns | Δ | bounded B/op | unbounded B/op |
|---:|---:|---:|---:|---:|---:|
| 64 B | 304.3 | 270.1 | **+12.7 %** | 584 / 3 | 560 / 2 |
| 256 B | 310.9 | 272.5 | **+14.1 %** | 584 / 3 | 560 / 2 |
| 4 KiB | 4 788 | 4 799 | −0.2 % | 10 440 / 11 | 10 416 / 10 |
| 64 KiB | 49 665 | 48 253 | +2.9 % | 138 184 / 18 | 138 160 / 17 |
| 1 MiB | 1 166 103 | 1 168 982 | −0.2 % | 2 228 046 / 26 | 2 228 022 / 25 |

**The bound costs exactly one allocation of 24 bytes — the `io.LimitReader`
struct — at every size, forever.** The delta is 24 B/op at 64 B, at 256 B, at
4 KiB, at 64 KiB and at 1 MiB, without exception. In time that is a flat ~38 ns,
which is 14 % of a 256-byte read and unmeasurable above 4 KiB. Stated where it belongs, as a
share of the `Decompress` it sits inside: **0.58 % of a 256 B gzip decompress**
(38 ns of 6 581 ns), and less than 0.01 % at 1 MiB.

### The refusal path

A 510× bomb — 65 780 compressed bytes expanding to 33 554 432 — refused at three
ceilings:

| cap | ns/op | B/op | allocs |
|---:|---:|---:|---:|
| 64 KiB | 117 980 | 146 582 | 18 |
| 1 MiB | 1 644 854 | 2 237 770 | 27 |
| 8 MiB | 18 434 126 | 21 383 292 | 33 |

**The cost tracks the cap and never the bomb.** 16× the cap costs 13.9× the
time; 8× the cap costs 11.2× (the super-linearity at the top is GC on 21 MB/op).
Refusing at 64 KiB costs 118 µs against the ~74 ms a full 32 MiB expansion would
have taken (the 8 MiB row × 4) — **627× cheaper than being wrong**.

One thing this table does *not* claim: the production ceiling is 256 MiB, and
this 32 MiB bomb is **under it**, so production would decompress it. That is
correct and deliberate — `bounded.go` is a layer-local ceiling, not a ratio
guard; the expansion-ratio guard lives at the `pkg/v1/codec` frame layer. The
rows measure the mechanism at caps small enough to fire it.

## 9. The optimisation that was refused, with its number

A fast path was added to all three `Decompress` functions and then removed:

```go
//: THE REFUSED BRANCH — nothing to append to, so skip the copy entirely.
if cap(dst) == 0 {
    return plain, nil
}
```

It was refused because it **silently abandons the append-to-`dst` contract** the
port documents in three places: a caller who passes a zero-length-but-owned
slice gets back a different backing array than the one they lent. No test was
sensitive to it — every call site in the tree passes `dst=nil` — and with the
branch in place the `append` below it is dead code, so the contract would have
been documented, untested and unreachable at the same time.

It was measured before it was removed, and re-measured here from scratch. Both
arms are local replicas differing in exactly this branch and nothing else; the
`appending` arm's B/op matches production `Decompress` to within 1 byte at 64 B
and 256 B, 4 bytes at 4 KiB and 266 bytes (0.008 %) at 1 MiB — the check that the
replica has not drifted.

| plaintext | appending ns | fast-path ns | Δ time | appending B/op | fast-path B/op | Δ bytes | allocs |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 64 B | 1 184 | 994 | **−16.0 %** | 649 | 585 | **−9.9 %** | 4 → 3 |
| 256 B | 6 286 | 6 061 | −3.6 % | 841 | 585 | **−30.4 %** | 4 → 3 |
| 4 KiB | 14 064 | 12 528 | −10.9 % | 14 552 | 10 450 | **−28.2 %** | 12 → 11 |
| 1 MiB | 2 105 366 | 1 235 564 | **−41.3 %** | 3 281 339 | 2 229 275 | **−32.1 %** | 29 → 28 |

**It bought a great deal.** At 1 MiB it removes one 1 MiB allocation and one
1 MiB copy: −32.1 % of bytes, and −41.3 % of time on this GC-bound VM (the
allocation figure is the reliable one; that row's own run-to-run spread is
7.5–14.1 %, so read the time as "large" rather than as 41.3 %). At 4 KiB and
256 B it removes a whole payload's worth of bytes.

**And it was still not taken**, for the contract reason above. But the
re-measurement also **corrects the record on the one point that had been used to
argue against it**:

> The refusal was recorded as costing a *regression* at 64 B — "725 vs 652 B/op,
> because `io.ReadAll`'s 512-byte floor exceeds the payload".

**That regression does not reproduce.** At 64 B the fast path allocates **585
B/op against 649** — 9.9 % *less*, not more, and one allocation fewer. The
mechanism cited was right and the conclusion drawn from it was wrong, because
`B/op` counts bytes **allocated**, and both arms allocate `io.ReadAll`'s 512-byte
floor identically; the appending arm merely allocates 64 more on top of it.

The real 64-byte defect is one `-benchmem` cannot see at all, and it is worse
than the one that was written down. Measured directly:

```
refused-fastpath retention at 64 B: ReadAll buffer len=64 cap=512 | append(nil,...) len=64 cap=64
```

**The fast path hands the caller a 64-byte result that pins a 512-byte array —
8× retention, held for as long as the caller holds the result.** The append
version returns `cap=64` and lets the 512-byte scratch die at once. For a
decompressor whose output is typically retained (parsed, cached, put in a map)
and whose scratch is not, that is the wrong trade at exactly the size where the
optimisation looked free. A refusal is better with its number; this one is better
with the *right* number.

## 10. Rows thrown away

Publishing a row that contradicts its neighbours is publishing noise. Three row
groups contradicted theirs; two were discarded and re-measured, one survived
investigation and is kept.

**`BenchmarkBoundedRead/*/1048576`, `-count=3`.** The 3-run medians came out
`bounded 1 317 546 ns` against `unbounded 1 434 067 ns` — the bounded read 8.8 %
*faster* than the unbounded one it is a strict superset of. Arithmetically
impossible: an `io.LimitReader` can only add work. The bounded row's own spread
was 18.6 %. Re-run at `-count=5`: `1 166 103` vs `1 168 982`, a 0.2 % gap in the
correct direction, spreads 8.2 % and 15.3 %. **Both original rows discarded; §8
reports the `-count=5` pair.** The cause was the run-to-run variance of a row
that allocates 2.2 MB per operation on a ballooned VM, not contention — no other
job was on the box.

**`BenchmarkDecompress/{gzip,flate,zlib}/text/1048576`, before vs after.** At
`-count=3` all three said pooling made 1 MiB text decompression 6–16 % *slower*,
while all three `random` rows said 16–20 % faster. A change that removes
allocation cannot systematically add 16 % of time, so the rows were re-run at
`-count=5` on both binaries:

| row | before (n=5) | after (n=5) | verdict |
|---|---:|---:|---|
| gzip / text | 2 105 962 | 2 236 678 | +6.2 %, spreads 13.5 % / 6.8 % — noise |
| flate / text | 2 118 279 | 2 118 871 | +0.03 % — identical |
| zlib / text | 2 184 158 | 2 455 986 | +12.4 %, spreads 9.0 % / 8.6 % — noise |
| gzip / random | 1 950 062 | 1 687 752 | 1.16× faster |
| flate / random | 1 881 659 | 1 458 272 | 1.29× faster |
| zlib / random | 2 089 021 | 2 024 266 | 1.03× faster |

**The honest statement is "no measurable effect at 1 MiB", not a speed-up and not
a regression**, and the reason is arithmetic: the pooled decoder is 36 764 B of
the 3.3 MB such a call allocates — **1.1 %** — so it cannot move a row whose own
spread is 9–14 %. §1.3's decompression table therefore stops at 64 KiB; §3's raw
matrix keeps the 1 MiB rows at `-count=3` for completeness, and the table above
is what to quote for them.

**`BenchmarkCompress/gzip/random/64` vs `/256`.** Kept, not discarded, after
investigation: the 3.2× inversion is a real property of `compress/flate` below
128 bytes and is documented in §4. It is the one row pair here that looked like
noise and was not.

## 11. Reading list for a caller

1. **Do not compress below 128 bytes** (§4). It costs 3–12 µs and makes the
   payload bigger, on every corpus, by construction.
2. **Pass a `dst` with capacity** (§5). Free 28–34 % on incompressible payloads.
3. **`BestSpeed` before `DefaultCompression`** (§6), unless a measurement on
   *your* corpus says otherwise. On this one it is 3.4× faster for the same
   ratio.
4. **Budget for the pool miss, not the pool hit** (§2, §7), if your service
   allocates heavily between compressions. The floor is the miss column.
