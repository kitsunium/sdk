<!-- generated from third-party/x-crypto/xchacha/xchacha_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./third-party/x-crypto/xchacha/` from the repo root; every figure below is the median of NINE samples over THREE such processes -->
# Benchmarks — `third-party/x-crypto/xchacha`

This package exists for one reason: XChaCha20-Poly1305's 192-bit nonce removes
the message-count ceiling that AES-256-GCM's 96-bit random nonce imposes. That
is a security property, and it is bought with a dependency
(`golang.org/x/crypto`) and with CPU. **This page prices the CPU half**, so the
trade can be made on numbers.

Every row is scheme against scheme through the *same* `core/crypto.AEAD` port,
in the same process, in the same run, under the same machine load. The reference
arm is `internal/service/crypto/aesgcm` — the SDK's dep-free default — sealing
the same plaintexts and producing the same self-framed box. Nothing here is
quoted from another page.

## The answer, in one table

Rate is **GB/s of plaintext** (10⁹ B/s), the only unit comparable across two
schemes with different framing overhead. `±` is the observed spread over the
nine samples.

| payload | XChaCha `Seal` | AES-GCM `Seal` | XChaCha vs AES-GCM |
|---|---:|---:|---:|
| 64 B | **760.1 ns** · 0.084 GB/s · ±2 % | 1 145 ns · 0.056 GB/s · ±6 % | **1.51× faster** |
| 1 KiB | 1 847 ns · 0.554 GB/s · ±3 % | 1 913 ns · 0.535 GB/s · ±12 % | 1.04× faster — parity |
| 64 KiB | 67 059 ns · 0.977 GB/s · ±21 % | **49 946 ns** · 1.312 GB/s · ±15 % | 1.34× slower † |
| 1 MiB | 1 430 257 ns · 0.733 GB/s · ±10 % | **707 543 ns** · 1.482 GB/s · ±9 % | **2.02× slower** |

| payload | XChaCha `Open` | AES-GCM `Open` | XChaCha vs AES-GCM |
|---|---:|---:|---:|
| 64 B | **592.3 ns** · 0.108 GB/s · ±5 % | 1 002 ns · 0.064 GB/s · ±9 % | **1.69× faster** |
| 1 KiB | 1 700 ns · 0.602 GB/s · ±6 % | 1 610 ns · 0.636 GB/s · ±10 % | 1.06× slower — parity |
| 64 KiB | 72 887 ns · 0.899 GB/s · ±14 % | **40 779 ns** · 1.607 GB/s · ±20 % | 1.79× slower † |
| 1 MiB | 1 247 521 ns · 0.841 GB/s · ±9 % | **679 070 ns** · 1.544 GB/s · ±4 % | 1.84× slower |

† **The 64 KiB rows are the least reliable numbers on this page** and are the one
place not to quote three significant figures — see "Rows thrown away" below.

**The crossover is at about 1 KiB.** Below it XChaCha is the faster call; above
it AES-GCM pulls away to a factor of two. So the operational reading is:

> **If your messages are a kilobyte or smaller, the extended nonce is free** —
> the two schemes are within 4 % of each other at 1 KiB, in both directions
> across runs, which is another way of saying the difference is unmeasurable
> here. At 1 MiB the extended nonce costs you half your AEAD throughput.

That is a genuinely unusual shape for a "slower cipher", and the next section is
about why it happens — because the reason changes what you should conclude.

## Why XChaCha wins at 64 B, and why that is not a property of the cipher

`core/crypto.AEAD` is `Seal(Key, plaintext, aad) ([]byte, error)`. It takes a
key, not a prepared cipher, so **both schemes rebuild their cipher on every
call**. That construction is not symmetric:

| per-call cipher construction | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `chacha20poly1305.NewX(key)` | **32.8** | 32 | 1 |
| `aes.NewCipher` + `cipher.NewGCM` | **744.4** | 1 280 | 2 |

**744.4 ns is 65 % of the 1 145 ns a 64-byte AES-GCM `Seal` costs.** That figure
was derived independently on `pkg/v1/crypto/BENCH.md` by a completely different
method — subtracting a hoisted-cipher arm rather than timing the constructor —
and landed on 66 %. Two methods, 65 % and 66 %: the claim is corroborated rather
than repeated.

Now take the construction away. Both benchmarks below hoist the cipher out of
the loop and seal 64 bytes with a fixed nonce:

| 64 B, cipher built once | ns/op | GB/s | B/op | allocs |
|---|---:|---:|---:|---:|
| bare XChaCha20-Poly1305 | 510.7 | 0.125 | 80 | 1 |
| bare AES-256-GCM | **213.4** | **0.300** | 80 | 1 |

**With the key schedule hoisted, AES-GCM is 2.39× faster at 64 bytes too.** So
XChaCha does not beat AES-GCM at small sizes; it beats *this port's shape*. The
SDK hands AES-256 a fresh 32-byte key every call and AES-256 answers by expanding
fourteen round keys and building a GHASH table, while XChaCha's constructor
copies 32 bytes and returns.

Two consequences, both worth stating plainly:

- **The small-message win is contingent.** If the SDK ever cached a
  `cipher.AEAD` per `Key`, the 64 B column would invert and AES-GCM would win at
  every size on this page. `pkg/v1/crypto/BENCH.md` explains why that cache does
  not exist — it would hold an expanded key schedule derived from the secret for
  as long as the cache lives, which is exactly what `Key.Zeroize` promises is
  impossible. **This page does not propose changing that**; it prices what the
  refusal buys XChaCha.
- **This is an AES-NI machine.** The CPU advertises `aes` and `pclmulqdq`, so
  AES-GCM runs on dedicated silicon and ChaCha20 runs on general SIMD. On a CPU
  *without* AES acceleration the bulk columns typically invert — that is
  ChaCha20's whole design brief — and **nothing on this page measures that case.**
  Do not carry the 2× to a machine whose `/proc/cpuinfo` has no `aes` flag.

## What the extended nonce costs per call: HChaCha20, ≈176 ns, in pure Go

XChaCha is ChaCha20 with an extra step: `HChaCha20` derives a subkey from the
key and the first 16 nonce bytes, and the remaining 8 bytes become the ChaCha20
nonce. The CPU profile of a 64-byte `Seal` (a dedicated 3 s profiling run, which
reported 774.0 ns/op) prices that step:

```
      flat  flat%   sum%        cum   cum%
    1300ms 32.50% 32.50%     1300ms 32.50%  golang.org/x/crypto/chacha20poly1305.chacha20Poly1305Seal
     720ms 18.00% 50.50%      720ms 18.00%  golang.org/x/crypto/chacha20.quarterRound (inline)
     420ms 10.50% 61.00%      420ms 10.50%  runtime.vgetrandom
     140ms  3.50% 64.50%      910ms 22.75%  golang.org/x/crypto/chacha20.hChaCha20
     120ms  3.00% 67.50%      270ms  6.75%  runtime.mallocgcSmallNoScanSC4
      90ms  2.25% 69.75%       90ms  2.25%  runtime.memmove
      80ms  2.00% 71.75%     3700ms 92.50%  github.com/kitsunium/sdk/third-party/x-crypto/xchacha.xChaCha.Seal
      60ms  1.50% 73.25%       60ms  1.50%  runtime.(*spanInlineMarkBits).init
      50ms  1.25% 74.50%      530ms 13.25%  crypto/internal/sysrand.Read
      50ms  1.25% 75.75%       50ms  1.25%  runtime.getMCache (inline)
      50ms  1.25% 77.00%      330ms  8.25%  runtime.mallocgc
```

`chacha20Poly1305Seal` is the fused amd64 assembly routine — 32.5 % — and it is
the part that scales with the payload. `hChaCha20` is **22.75 % cumulative** of a
64-byte seal, and `quarterRound` sitting at 18 % *inlined* is the giveaway:
`golang.org/x/crypto/chacha20` ships `chacha_arm64.s`, `chacha_ppc64x.s` and
`chacha_s390x.s`, and **no amd64 assembly at all** — `HChaCha20` is only defined
in `chacha_generic.go`. So on amd64 the subkey derivation runs twenty rounds of
pure-Go ARX while the AEAD body runs in hand-written assembly.

At 774 ns/op that is **≈176 ns per call, paid identically at 64 B and at 1 MiB**.
It is the price of the extended nonce, it is a constant, and it is why the
per-byte columns converge as the payload grows.

`crypto/internal/sysrand.Read` at **13.25 %** is the other fixed cost: 24 bytes
of entropy per `Seal` against AES-GCM's 12. Both are `vgetrandom`, so it is a
vDSO call rather than a syscall, and neither scheme can avoid it — a random
nonce is the whole construction.

## The allocation columns close exactly

Unlike the timings, these did not vary at all across the nine samples.

| payload | XChaCha `Seal` | AES-GCM `Seal` | XChaCha `Open` | AES-GCM `Open` |
|---|---:|---:|---:|---:|
| 64 B | 200 B · **4** | 1 424 B · 5 | 128 B · **3** | 1 376 B · 4 |
| 1 KiB | 1 240 B · 4 | 2 480 B · 5 | 1 088 B · 3 | 2 336 B · 4 |
| 64 KiB | 73 816 B · 4 | 75 056 B · 5 | 65 600 B · 3 | 66 848 B · 4 |
| 1 MiB | 1 056 858 B · 4 | 1 058 099 B · 5 | 1 048 644 B · 3 | 1 049 891 B · 4 |

The allocation profile of a 64-byte `Seal` names all four:

```
      flat  flat%   sum%        cum   cum%
   8993071 47.87% 47.87%   18741848 99.77%  …/third-party/x-crypto/xchacha.xChaCha.Seal
   4964503 26.43% 74.30%    4964503 26.43%  golang.org/x/crypto/chacha20poly1305.NewX
   4784274 25.47% 99.77%    4784274 25.47%  bytes.Clone (inline)
         0     0% 99.77%    4784274 25.47%  …/internal/core/crypto.Key.Bytes (inline)
         0     0% 99.77%    4964503 26.43%  …/third-party/x-crypto/xchacha.newAEAD (inline)
```

Two from `Seal` itself, one from `NewX`, one from `Key.Bytes`'s defensive
`bytes.Clone`. **The bytes reconcile to the last byte**, which is the check that
says the profile and the `-benchmem` column describe the same run:

| | XChaCha | AES-GCM |
|---|---:|---:|
| the box (`header+nonce+ct+tag`, rounded to a size class) | 112 | 96 |
| `Key.Bytes` defensive clone | 32 | 32 |
| the nonce, escaping through the `cipher.AEAD` interface | 24 | 16 |
| the cipher (measured directly, above) | 32 | 1 280 |
| **total** | **200** ✓ | **1 424** ✓ |

Both columns match `-benchmem` exactly. The gap between the two schemes —
**1 224 bytes at 64 B and 1 240 at every larger size**, where the two boxes land
in the same size class — is one thing and one thing only: the AES key schedule
plus the GHASH tables, rebuilt per call.

`Open` is one allocation and one size class cheaper than `Seal` in both schemes —
it draws no nonce — and it is *faster* than `Seal` at every size for XChaCha,
which is the same asymmetry `pkg/v1/crypto/BENCH.md` records for AES-GCM.

## Rows thrown away, and what was wrong with them

**Three sets, and the third is why this page publishes nine samples.**

**1. A per-byte rate that fell as the payload grew.** The first `Seal` ladder had
XChaCha at 0.989 GB/s at 64 KiB and 0.783 GB/s at 1 MiB — 21 % *slower per byte*
on the larger payload, which a stream cipher does not do. A diagnostic pair with
`GOGC=off` found the cause, and it was not XChaCha:

| single diagnostic process, `-count=3`, minima | GC on | `GOGC=off` | GC's share |
|---|---:|---:|---:|
| XChaCha `Seal` 1 MiB | 1 339 586 ns | **953 135 ns** | **28.8 %** |
| AES-GCM `Seal` 1 MiB | 670 386 ns | **485 723 ns** | **27.5 %** |

**At 1 MiB, roughly 28 % of the wall clock is the garbage collector, not the
cipher** — the port returns a freshly allocated megabyte on every call. With the
collector out of the way both schemes are monotone in payload size, as physics
requires. Crucially the *ratio* barely moves: 1.96× with GC off against 2.02×
with it on, so the conclusion survives even though the absolute rates do not.

**2. `Hash`-style position artefacts.** Rows measured late in a 20-row run read
systematically differently from the same rows measured early. Not reproducible
across processes; discarded.

**3. An AES-GCM 64 KiB row that was 20 % too fast.** The first run put
`AESGCMSeal/64KiB` at 40 493–40 772 ns across three samples — a 0.7 % spread,
which reads like a precise measurement. Nine later samples over three further
processes landed at **48 689–55 936 ns**, median 49 946. The published ratio at
that size moved from 1.63× to 1.34× purely because of which process measured it.

That is the finding that set this page's protocol. **The 64 KiB rows are the
least stable on this box** — 15–21 % spread over nine samples for both schemes,
against 2–6 % at 64 B and 4–10 % at 1 MiB. A plausible mechanism, offered as a
hypothesis and *not* tested here: at 64 KiB the per-op allocation is 73–75 KB,
just above Go's 32 KB large-object threshold, so every call goes through the page
allocator whose span and scavenger state differs between processes; at 1 KiB the
small-object path is used, and at 1 MiB the cost is dominated by the bytes
themselves.

Every figure in the tables above is therefore the **median of nine samples over
three separate processes**, and no conclusion on this page rests on a difference
smaller than the spread quoted beside it. The two that matter — a 1.5× win at
64 B and a 2.0× loss at 1 MiB — are both far outside it.

## When to swap — the security half, priced

The reason to take this dependency is not speed, so the speed table alone cannot
decide it. With random 96-bit nonces, AES-GCM must not exceed ~2³² invocations
per key before the birthday bound makes a nonce collision likelier than
acceptable; a 192-bit nonce puts that ceiling out of reach entirely.

| sustained `Seal` rate | 2³² messages reached in | swap costs (at 1 KiB) |
|---:|---:|---|
| 100 /s | 497 days | within noise |
| 1 000 /s | 49.7 days | within noise |
| 10 000 /s | **4.97 days** | within noise |
| 100 000 /s | **11.9 hours** | within noise |

**At small message sizes the extended nonce is free and removes a key-rotation
deadline measured in days.** That is the case for this package, and it is the row
a caller should read.

Above 64 KiB the arithmetic reverses: the same guarantee costs 1.3–2.0× of AEAD
throughput, and a fleet sealing megabytes is usually better served by rotating
the key — or by `crypto.SealStream`, which reframes into 64 KiB chunks and
therefore draws a fresh nonce per chunk anyway.

## Refused

Two optimisations are visible in the numbers above and are **not** taken. Both
are recorded here so the next reader does not have to rediscover why.

- **Caching the `cipher.AEAD` per `Key`.** It would remove 32.8 ns and one
  allocation from every XChaCha call and 744.4 ns and 1 280 bytes from every
  AES-GCM call — the single largest number on this page. Refused: it retains key
  material derived from the secret for the cache's lifetime, which defeats
  `Key.Zeroize`, whose entire promise is that clearing one copy clears the
  secret. That is an ADR 0013 design question, not a benchmark's call.
- **Hoisting the nonce buffer out of `Seal`.** The 24-byte nonce escapes to the
  heap because it is passed as a slice to an interface method, and a
  package-level scratch buffer would remove that allocation. Refused: a nonce
  buffer shared across calls is the exact shape a nonce-reuse bug takes, it would
  be unsafe under concurrent `Seal`, and no allocation on this page is worth
  making that mistake reachable. **The current code is right:** a fresh stack
  array per call, filled from `crypto/rand`, never reused.

Nothing in this package was changed to produce this report.

## Reproducibility envelope

> **Numbers vary across machines, and this one is shared.** Every figure quoted
> above is the **median of nine samples over three separate processes**, with the
> observed spread printed beside it; one of the three is reproduced verbatim
> below. Within one process the samples are correlated — they share a heap state,
> a scavenger state and a slice of this VM's luck — so a single `-count=3` run
> reports a precision it does not have. That is not a hypothetical: it is how the
> 64 KiB row above was wrong by 20 %.
>
> (The sibling `pkg/v1/crypto/BENCH.md` quotes *minima* from a single process;
> this page quotes medians over three, so a direct cell-to-cell comparison across
> the two files is off by roughly the spread quoted here.)
>
> **The AES-NI caveat is not a footnote.** This CPU has hardware AES and
> carry-less multiply. The bulk-size conclusions on this page are specific to
> that; on a CPU without them the ordering is expected to invert, and this page
> does not measure it.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, `sha_ni`, `sse4_2`, `avx2` |
| RAM                | 15.6 GiB (ballooned VM; balloon floor 8 GiB) |
| Load during the runs | 0.03–0.55 (one-minute average, machine otherwise idle) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Library            | `golang.org/x/crypto` v0.55.0 |
| Reference arm      | `internal/service/crypto/aesgcm` (same repo, same run) |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `f6082f7` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, × 3 processes |

Go's own `MB/s` column below is decimal (10⁶ B/s); the GB/s figures in this page
are that column divided by 1 000.

## Results

One of the three processes, verbatim.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/third-party/x-crypto/xchacha
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkSeal/64B-8             	 1579646	       759.4 ns/op	  84.27 MB/s	     200 B/op	       4 allocs/op
BenchmarkSeal/64B-8             	 1577230	       760.1 ns/op	  84.20 MB/s	     200 B/op	       4 allocs/op
BenchmarkSeal/64B-8             	 1588912	       758.5 ns/op	  84.38 MB/s	     200 B/op	       4 allocs/op
BenchmarkSeal/1KiB-8            	  651190	      1829 ns/op	 559.73 MB/s	    1240 B/op	       4 allocs/op
BenchmarkSeal/1KiB-8            	  669284	      1836 ns/op	 557.59 MB/s	    1240 B/op	       4 allocs/op
BenchmarkSeal/1KiB-8            	  645403	      1844 ns/op	 555.30 MB/s	    1240 B/op	       4 allocs/op
BenchmarkSeal/64KiB-8           	   18216	     66888 ns/op	 979.79 MB/s	   73816 B/op	       4 allocs/op
BenchmarkSeal/64KiB-8           	   17918	     67059 ns/op	 977.29 MB/s	   73816 B/op	       4 allocs/op
BenchmarkSeal/64KiB-8           	   17910	     67974 ns/op	 964.13 MB/s	   73816 B/op	       4 allocs/op
BenchmarkSeal/1MiB-8            	     792	   1404219 ns/op	 746.73 MB/s	 1056860 B/op	       4 allocs/op
BenchmarkSeal/1MiB-8            	     873	   1403323 ns/op	 747.21 MB/s	 1056859 B/op	       4 allocs/op
BenchmarkSeal/1MiB-8            	     834	   1507787 ns/op	 695.44 MB/s	 1056859 B/op	       4 allocs/op
BenchmarkOpen/64B-8             	 2074676	       578.5 ns/op	 110.63 MB/s	     128 B/op	       3 allocs/op
BenchmarkOpen/64B-8             	 2081631	       578.6 ns/op	 110.60 MB/s	     128 B/op	       3 allocs/op
BenchmarkOpen/64B-8             	 2023538	       588.0 ns/op	 108.85 MB/s	     128 B/op	       3 allocs/op
BenchmarkOpen/1KiB-8            	  705610	      1635 ns/op	 626.39 MB/s	    1088 B/op	       3 allocs/op
BenchmarkOpen/1KiB-8            	  689991	      1629 ns/op	 628.61 MB/s	    1088 B/op	       3 allocs/op
BenchmarkOpen/1KiB-8            	  741706	      1635 ns/op	 626.37 MB/s	    1088 B/op	       3 allocs/op
BenchmarkOpen/64KiB-8           	   17562	     67985 ns/op	 963.98 MB/s	   65600 B/op	       3 allocs/op
BenchmarkOpen/64KiB-8           	   17776	     67907 ns/op	 965.09 MB/s	   65600 B/op	       3 allocs/op
BenchmarkOpen/64KiB-8           	   16950	     69376 ns/op	 944.65 MB/s	   65600 B/op	       3 allocs/op
BenchmarkOpen/1MiB-8            	    1003	   1208033 ns/op	 868.00 MB/s	 1048644 B/op	       3 allocs/op
BenchmarkOpen/1MiB-8            	     979	   1183786 ns/op	 885.78 MB/s	 1048642 B/op	       3 allocs/op
BenchmarkOpen/1MiB-8            	     996	   1233362 ns/op	 850.18 MB/s	 1048645 B/op	       3 allocs/op
BenchmarkAESGCMSeal/64B-8       	  901124	      1134 ns/op	  56.43 MB/s	    1424 B/op	       5 allocs/op
BenchmarkAESGCMSeal/64B-8       	 1000000	      1117 ns/op	  57.28 MB/s	    1424 B/op	       5 allocs/op
BenchmarkAESGCMSeal/64B-8       	  933039	      1120 ns/op	  57.12 MB/s	    1424 B/op	       5 allocs/op
BenchmarkAESGCMSeal/1KiB-8      	  675046	      1805 ns/op	 567.42 MB/s	    2480 B/op	       5 allocs/op
BenchmarkAESGCMSeal/1KiB-8      	  698640	      1759 ns/op	 582.11 MB/s	    2480 B/op	       5 allocs/op
BenchmarkAESGCMSeal/1KiB-8      	  682940	      1791 ns/op	 571.76 MB/s	    2480 B/op	       5 allocs/op
BenchmarkAESGCMSeal/64KiB-8     	   24999	     48725 ns/op	1345.01 MB/s	   75056 B/op	       5 allocs/op
BenchmarkAESGCMSeal/64KiB-8     	   24740	     48912 ns/op	1339.87 MB/s	   75056 B/op	       5 allocs/op
BenchmarkAESGCMSeal/64KiB-8     	   23548	     49921 ns/op	1312.80 MB/s	   75056 B/op	       5 allocs/op
BenchmarkAESGCMSeal/1MiB-8      	    1582	    701412 ns/op	1494.95 MB/s	 1058099 B/op	       5 allocs/op
BenchmarkAESGCMSeal/1MiB-8      	    1443	    732002 ns/op	1432.48 MB/s	 1058099 B/op	       5 allocs/op
BenchmarkAESGCMSeal/1MiB-8      	    1622	    724188 ns/op	1447.93 MB/s	 1058099 B/op	       5 allocs/op
BenchmarkAESGCMOpen/64B-8       	 1191518	      1003 ns/op	  63.79 MB/s	    1376 B/op	       4 allocs/op
BenchmarkAESGCMOpen/64B-8       	 1224457	       977.5 ns/op	  65.47 MB/s	    1376 B/op	       4 allocs/op
BenchmarkAESGCMOpen/64B-8       	 1256382	       959.7 ns/op	  66.69 MB/s	    1376 B/op	       4 allocs/op
BenchmarkAESGCMOpen/1KiB-8      	  740684	      1569 ns/op	 652.51 MB/s	    2336 B/op	       4 allocs/op
BenchmarkAESGCMOpen/1KiB-8      	  769688	      1588 ns/op	 644.83 MB/s	    2336 B/op	       4 allocs/op
BenchmarkAESGCMOpen/1KiB-8      	  825339	      1581 ns/op	 647.57 MB/s	    2336 B/op	       4 allocs/op
BenchmarkAESGCMOpen/64KiB-8     	   29212	     40779 ns/op	1607.10 MB/s	   66848 B/op	       4 allocs/op
BenchmarkAESGCMOpen/64KiB-8     	   28874	     41213 ns/op	1590.18 MB/s	   66848 B/op	       4 allocs/op
BenchmarkAESGCMOpen/64KiB-8     	   29718	     40685 ns/op	1610.80 MB/s	   66848 B/op	       4 allocs/op
BenchmarkAESGCMOpen/1MiB-8      	    1875	    670986 ns/op	1562.74 MB/s	 1049892 B/op	       4 allocs/op
BenchmarkAESGCMOpen/1MiB-8      	    1686	    677876 ns/op	1546.86 MB/s	 1049892 B/op	       4 allocs/op
BenchmarkAESGCMOpen/1MiB-8      	    1767	    687385 ns/op	1525.46 MB/s	 1049891 B/op	       4 allocs/op
BenchmarkNewAEADXChaCha-8       	38488740	        31.44 ns/op	      32 B/op	       1 allocs/op
BenchmarkNewAEADXChaCha-8       	36400605	        32.28 ns/op	      32 B/op	       1 allocs/op
BenchmarkNewAEADXChaCha-8       	38339821	        32.18 ns/op	      32 B/op	       1 allocs/op
BenchmarkNewAEADAESGCM-8        	 1691090	       703.0 ns/op	    1280 B/op	       2 allocs/op
BenchmarkNewAEADAESGCM-8        	 1627188	       744.4 ns/op	    1280 B/op	       2 allocs/op
BenchmarkNewAEADAESGCM-8        	 1582160	       761.6 ns/op	    1280 B/op	       2 allocs/op
BenchmarkBareXChaChaSeal64B-8   	 2352242	       505.4 ns/op	 126.64 MB/s	      80 B/op	       1 allocs/op
BenchmarkBareXChaChaSeal64B-8   	 2431524	       500.9 ns/op	 127.76 MB/s	      80 B/op	       1 allocs/op
BenchmarkBareXChaChaSeal64B-8   	 2333239	       508.4 ns/op	 125.88 MB/s	      80 B/op	       1 allocs/op
BenchmarkBareAESGCMSeal64B-8    	 5767864	       209.8 ns/op	 304.98 MB/s	      80 B/op	       1 allocs/op
BenchmarkBareAESGCMSeal64B-8    	 5740602	       207.8 ns/op	 307.92 MB/s	      80 B/op	       1 allocs/op
BenchmarkBareAESGCMSeal64B-8    	 5735259	       209.4 ns/op	 305.70 MB/s	      80 B/op	       1 allocs/op
PASS
ok  	github.com/kitsunium/sdk/third-party/x-crypto/xchacha
```
