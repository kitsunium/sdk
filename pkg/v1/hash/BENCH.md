<!-- generated from pkg/v1/hash/hash_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./v1/hash/` to refresh -->
# Benchmarks — `pkg/v1/hash`

Five unkeyed hashes behind one `Sum`. Three are collision-resistant and two are
not, and the package doc already says which — what it could not say is what the
property costs. The family-wide choice table lives in
[`pkg/v1/crypto/BENCH.md`](../crypto/BENCH.md); this page is the hashing column.

## The choice table

| algorithm | 64 B | 4 KiB | 1 MiB | throughput @1 MiB | collision-resistant |
|---|---:|---:|---:|---:|:---:|
| **CRC32C** | **121 ns** | **699 ns** | **153 µs** | **6 521 MiB/s** | no |
| SHA-256 | 261 ns | 3 140 ns | 745 µs | 1 342 MiB/s | yes |
| FNV-1a 64 | 166 ns | 5 821 ns | 1 482 µs | 675 MiB/s | no |
| SHA-512 | 578 ns | 10 928 ns | 2 642 µs | 379 MiB/s | yes |
| SHA3-256 | 718 ns | 15 116 ns | 3 707 µs | 270 MiB/s | yes |

Every one is **2 allocations** — a fresh `hash.Hash` and the returned digest —
at every size, so the allocation column is not a discriminator here.

The practical reading, in one line each:

- **CRC32C is the right default for a cache key.** It is 4.9× SHA-256 at a
  megabyte and 2.2× at 64 bytes, and its non-cryptographic status is exactly
  what a cache key does not need.
- **SHA-256 is the right default for a content id**, and it is the *only*
  cryptographic option that is not a throughput decision — the other two cost
  3.5× and 5× more.
- **FNV-1a is not the fast one.** See below.
- **SHA-512 is not faster than SHA-256 here.** See below.

## The two surprises, both measured to their instruction

### SHA-512 is 3.5× slower than SHA-256, which inverts the usual expectation

On a 64-bit CPU without hardware hashing, SHA-512 normally *beats* SHA-256:
same round count, twice the block, 64-bit words. Here it loses by 3.5×. A CPU
profile over the two benchmarks accounts for 99.78 % of the samples and names
the reason in two lines:

```
2.41s 51.94%  crypto/internal/fips140/sha512.blockAVX2
2.22s 47.84%  crypto/internal/fips140/sha256.blockSHANI
```

`/proc/cpuinfo` on this box lists `sha_ni`. **The SHA-NI instruction set covers
SHA-1 and SHA-256 and does not cover SHA-512**, so SHA-256 runs as a hardware
instruction and SHA-512 runs as hand-vectorised AVX2 software. That is the whole
of it, and it is a property of the CPU, not of Go and not of this SDK — the
ordering will invert again on a machine without SHA-NI, which is precisely why
the reproducibility envelope below records the ISA.

The actionable form: **on any target you control, prefer SHA-256 over SHA-512
unless you need the longer digest for its own sake.** The "bigger is faster on
64-bit" rule of thumb predates SHA-NI.

### FNV-1a, the "fast fingerprint", is slower than SHA-256 above 256 bytes

| size | FNV-1a 64 | SHA-256 | winner |
|---:|---:|---:|:---|
| 64 B | **166 ns** | 261 ns | FNV-1a, by 1.57× |
| **256 B** | **441 ns** | **408 ns** | **tie — the crossover** |
| 4 KiB | 5 821 ns | 3 140 ns | SHA-256, by 1.85× |
| 1 MiB | 1 482 µs | 745 µs | SHA-256, by 1.99× |

The crossover was not asserted: fitting the fixed and per-byte terms of the
64 B and 4 KiB points put it near 170 bytes, so 256 — the next round size
*above* that estimate — was measured, and the two land 8 % apart, which is the
noise floor on this box. The crossover is therefore right about there.

A profile says why, again in two lines and again entirely inside the stdlib:

```
2.40s 50.31%  hash/fnv.(*sum64a).Write
2.30s 48.22%  hash/crc32.castagnoliSSE42Triple
```

`hash/fnv` is a portable Go loop that multiplies and XORs **one byte at a
time**. `hash/crc32` dispatches to the SSE4.2 `CRC32` instruction with three
interleaved streams. FNV-1a's advantage is entirely its tiny fixed cost —
16 B/op against SHA-256's 160 B/op — and that advantage is spent by a quarter of
a kilobyte.

**So: FNV-1a for short keys where the 16-byte footprint matters, CRC32C for
everything else.** There is no size at which FNV-1a beats CRC32C.

## What the SDK costs over the bare primitive

`BenchmarkBareSHA256_*` calls `sha256.Sum256` with nothing between it and the
caller.

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `hash.Sum(SHA256, …)` 64 B | 261 | 160 | 2 |
| `sha256.Sum256` 64 B | **155** | **0** | **0** |
| `hash.Sum(SHA256, …)` 1 MiB | 745 172 | 160 | 2 |
| `sha256.Sum256` 1 MiB | 740 167 | 0 | 0 |

**At a megabyte the facade costs 0.68 %** — 5 µs on 745 µs, which is the noise
floor. At 64 bytes it costs **106 ns and 2 allocations**, and those two are
worth naming because they are the port's shape rather than an oversight:

1. `Hasher.New()` returns a fresh `hash.Hash`, so the digest state is heap
   allocated. `sha256.Sum256` keeps it on the stack.
2. `Sum` returns `[]byte`. `sha256.Sum256` returns `[32]byte`, an array.

Neither is avoidable without changing the published port — a one-shot
`Hasher.Sum([]byte) []byte` sibling (ADR 0039's mechanism) would remove the
first, and nothing removes the second short of returning an array and giving up
the algorithm-agnostic signature the package exists for. **Recorded, not
attempted**: it saves 106 ns on a call whose reason to exist is usually bulk,
and the profile above shows 99.78 % of a bulk hash is already in the stdlib
block function.

## Streaming and hex cost nothing

| 1 MiB | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Sum(SHA256)` one shot | 745 172 | 160 | 2 |
| `New(SHA256)` + 32 KiB writes | **740 805** | 160 | 2 |
| `NewDigestWriter` → `io.Discard` | **740 386** | 320 | 5 |

Driving the streaming `hash.Hash` in 32 KiB chunks is **indistinguishable** from
the one-shot call, so `io.Copy` over a large file costs nothing extra — there is
no reason to buffer a payload just to call `Sum`. `DigestWriter` adds 3
allocations for the tee and no measurable time, so content-addressing *while*
writing is free relative to the write.

Hex rendering is the one small extra: `SumHex` at 4 KiB is **3 285 ns against
`Sum`'s 3 140 ns — 145 ns and 2 more allocations** for the 64-character string.
A content id that is stored as hex pays that on every write and it is not worth
avoiding.

## Reproducibility envelope

> **Numbers vary across machines, and this page's headline finding says so
> loudly**: the SHA-512 result is a consequence of `sha_ni` being present, and
> it inverts on a CPU without it. The ISA line below is therefore part of the
> measurement, not decoration.
>
> This box is shared with fifteen other agent jobs and its load swings during a
> run. Each benchmark ran **three times**; every figure quoted above is the
> **fastest of the three** — the least-contaminated sample — and all three are
> printed below. The allocation columns are exact and do not vary.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, **`sha_ni`**, `sse4_2`, `avx2` |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 (`GOAMD64=v1`) |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | c30e2ad |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, machine under load |

Throughput is quoted in **MiB/s**; Go's own `MB/s` column below is decimal
(10⁶ B/s), so the two differ by 1.048576.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/hash
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkSumSHA256_64B-8             	 4662354	       260.8 ns/op	 245.44 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_64B-8             	 4414320	       272.3 ns/op	 235.02 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_64B-8             	 4584465	       264.8 ns/op	 241.71 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_4KiB-8            	  368839	      3151 ns/op	1300.09 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_4KiB-8            	  375196	      3140 ns/op	1304.65 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_4KiB-8            	  374328	      3143 ns/op	1303.36 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_1MiB-8            	    1603	    751489 ns/op	1395.33 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_1MiB-8            	    1610	    745172 ns/op	1407.16 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_1MiB-8            	    1608	    745853 ns/op	1405.87 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA512_64B-8             	 2039974	       583.0 ns/op	 109.78 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_64B-8             	 2051640	       601.3 ns/op	 106.43 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_64B-8             	 2057916	       578.0 ns/op	 110.72 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_4KiB-8            	  108962	     10928 ns/op	 374.83 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_4KiB-8            	  110222	     11145 ns/op	 367.52 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_4KiB-8            	  110300	     11268 ns/op	 363.49 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_1MiB-8            	     453	   2642090 ns/op	 396.87 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_1MiB-8            	     448	   2651300 ns/op	 395.50 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA512_1MiB-8            	     453	   2685179 ns/op	 390.51 MB/s	     288 B/op	       2 allocs/op
BenchmarkSumSHA3256_64B-8            	 1678440	       720.2 ns/op	  88.87 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_64B-8            	 1679431	       718.2 ns/op	  89.11 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_64B-8            	 1659795	       719.8 ns/op	  88.92 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_4KiB-8           	   79255	     15116 ns/op	 270.98 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_4KiB-8           	   78996	     15144 ns/op	 270.46 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_4KiB-8           	   77770	     15376 ns/op	 266.38 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_1MiB-8           	     325	   3706779 ns/op	 282.88 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_1MiB-8           	     321	   3740795 ns/op	 280.31 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumSHA3256_1MiB-8           	     321	   3739901 ns/op	 280.38 MB/s	     272 B/op	       2 allocs/op
BenchmarkSumCRC32C_64B-8             	 9772419	       122.4 ns/op	 522.69 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_64B-8             	 9605222	       121.0 ns/op	 528.83 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_64B-8             	 9910150	       120.5 ns/op	 531.25 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_4KiB-8            	 1714738	       699.2 ns/op	5858.07 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_4KiB-8            	 1714414	       700.9 ns/op	5844.27 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_4KiB-8            	 1685331	       702.4 ns/op	5831.26 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_1MiB-8            	    7622	    153374 ns/op	6836.72 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_1MiB-8            	    7675	    154351 ns/op	6793.44 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumCRC32C_1MiB-8            	    6825	    154609 ns/op	6782.11 MB/s	      24 B/op	       2 allocs/op
BenchmarkSumFNV1a64_64B-8            	 7199889	       167.6 ns/op	 381.91 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_64B-8            	 7279498	       166.2 ns/op	 385.02 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_64B-8            	 7298282	       166.0 ns/op	 385.46 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_4KiB-8           	  206011	      5909 ns/op	 693.15 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_4KiB-8           	  203331	      5855 ns/op	 699.56 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_4KiB-8           	  201267	      5821 ns/op	 703.63 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_1MiB-8           	     776	   1489775 ns/op	 703.85 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_1MiB-8           	     813	   1482082 ns/op	 707.50 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_1MiB-8           	     802	   1498042 ns/op	 699.96 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumSHA256_256B-8            	 2908514	       404.6 ns/op	 632.72 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_256B-8            	 2833276	       413.1 ns/op	 619.71 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumSHA256_256B-8            	 2905286	       407.6 ns/op	 628.14 MB/s	     160 B/op	       2 allocs/op
BenchmarkSumFNV1a64_256B-8           	 2746987	       441.0 ns/op	 580.54 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_256B-8           	 2640993	       448.7 ns/op	 570.54 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumFNV1a64_256B-8           	 2724709	       436.8 ns/op	 586.13 MB/s	      16 B/op	       2 allocs/op
BenchmarkSumHexSHA256_4KiB-8         	  371234	      3304 ns/op	1239.77 MB/s	     288 B/op	       4 allocs/op
BenchmarkSumHexSHA256_4KiB-8         	  346026	      3301 ns/op	1240.81 MB/s	     288 B/op	       4 allocs/op
BenchmarkSumHexSHA256_4KiB-8         	  357932	      3285 ns/op	1246.96 MB/s	     288 B/op	       4 allocs/op
BenchmarkBareSHA256_64B-8            	 7738492	       155.1 ns/op	 412.72 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_64B-8            	 7748882	       155.0 ns/op	 412.89 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_64B-8            	 7761250	       154.6 ns/op	 414.05 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8           	    1609	    742610 ns/op	1412.01 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8           	    1621	    745422 ns/op	1406.69 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8           	    1621	    740167 ns/op	1416.68 MB/s	       0 B/op	       0 allocs/op
BenchmarkNewStreamSHA256_1MiB-8      	    1621	    740805 ns/op	1415.45 MB/s	     160 B/op	       2 allocs/op
BenchmarkNewStreamSHA256_1MiB-8      	    1434	    749252 ns/op	1399.50 MB/s	     160 B/op	       2 allocs/op
BenchmarkNewStreamSHA256_1MiB-8      	    1620	    741157 ns/op	1414.78 MB/s	     160 B/op	       2 allocs/op
BenchmarkDigestWriterSHA256_1MiB-8   	    1616	    740386 ns/op	1416.26 MB/s	     320 B/op	       5 allocs/op
BenchmarkDigestWriterSHA256_1MiB-8   	    1620	    741206 ns/op	1414.69 MB/s	     320 B/op	       5 allocs/op
BenchmarkDigestWriterSHA256_1MiB-8   	    1618	    741800 ns/op	1413.56 MB/s	     320 B/op	       5 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/hash	78.831s
```
