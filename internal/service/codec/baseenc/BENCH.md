<!-- generated from internal/service/codec/baseenc/baseenc_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./codec/baseenc/` to refresh -->
# Benchmarks — `internal/service/codec/baseenc`

Nine base-N encodings behind one registry. A caller picks one of them, and
until this file there was nothing to pick on: `pkg/v1/codec/BENCH.md` reports
`0` for `base58` and `base62` because the cross-format harness excludes them,
and the seven others were never compared to each other at their own layer.

This report is a **choice table**. Section 1 is the table; sections 2-5 are the
three findings that writing it produced, two of which are now fixed.

## 1. The choice table

One base-N encode of the same 1 KiB payload, **without** the JSON envelope, so
what is measured is the alphabet. `x-expand` is the encoded length divided by
the raw length — the other half of every decision this table serves.

| variant | encode | decode | expansion | encode allocs |
|---|---:|---:|---:|---:|
| `base64` | **1 557 ns** | 1 814 ns | 1.336× | 1 |
| `base64url` | 1 577 ns | 1 808 ns | 1.336× | 1 |
| `base32` | 1 552 ns | 5 902 ns | 1.602× | 1 |
| `hex` | 2 139 ns | 1 507 ns | 2.000× | 1 |
| `base16` | 2 647 ns | 1 505 ns | 2.000× | 1 |
| `base45` | 2 727 ns | 1 850 ns | 1.500× | 1 |
| `ascii85` | 3 182 ns | 4 406 ns | **1.250×** | 1 |
| `base62` | **4 306 150 ns** | 1 365 026 ns | 1.344× | 3 |
| `base58` | **4 337 669 ns** | 1 384 209 ns | 1.365× | 3 |

**The one sentence this table is for: `base58` and `base62` cost ~2 770× what
`base64` costs to encode the same kilobyte, and buy 0.03 expansion points
against it — so choose them for what an alphabet *looks like* (no `0`/`O`/`I`/`l`,
no `+`/`/`, double-clickable, QR-safe), never for size and never on a hot path.**

Everything else in the table is close enough that expansion decides:
`ascii85` is the most compact at 1.250× and costs 2× `base64`; `base64` is the
default for a reason; `hex`/`base16` double the payload and are for
human-readable output, not transport.

`base32`'s decode is 3.8× its encode and 3.3× `base64`'s — the stdlib's base32
decoder is simply slower, and it allocates twice. If a payload is decoded more
often than encoded, that is worth knowing before choosing base32 for its
case-insensitive alphabet.

### What the JSON envelope adds

The public `Marshal` runs `encoding/json` first, and `BenchmarkMarshalJSONStep`
prices that step alone at **1 597 ns / 2 allocs** for this payload. So a
`base64` `Marshal` at 4 043 ns is roughly 1 597 ns of JSON, ~1 900 ns of base-N
over the ~1.37× longer JSON text, and a copy out of the pooled buffer. For the
seven block encodings the envelope is between a quarter and half the bill; for
`base58` it is 0.02 % of it.

## 2. The quadratic variants are quadratic, and the doc understated it by 10×

`base58`/`base62` treat the whole input as one integer. The sweep, at the four
sizes up to `maxConvBytes`:

| raw bytes | base64 encode | base58 encode | ratio | base64 decode | base58 decode |
|---:|---:|---:|---:|---:|---:|
| 64 | 145.0 ns | 17 525 ns | 121× | 215.0 ns | 5 747 ns |
| 256 | 451.4 ns | 272 421 ns | 603× | 698.3 ns | 88 034 ns |
| 1 024 | 1 573 ns | 4 362 386 ns | 2 773× | 2 228 ns | 1 393 386 ns |
| 4 096 | 6 541 ns | **69 297 872 ns** | 10 594× | 8 706 ns | **22 286 273 ns** |

`base64` is linear: 64× the input costs 45× the time. `base58` is quadratic:
64× the input costs **3 954×** the time. 4 KiB — an input `maxConvBytes`
explicitly permits — encodes in **69 ms** and decodes in **22 ms**.

`CLAUDE.md` said a few KiB "bounds the work to the low-millisecond range". It
does not, and the file now says 69 ms. This matters beyond tidiness: the cap is
a CWE-400 defence, and a defence sized against a wrong estimate of what it
lets through is not sized at all. **The cap is not being widened here** — it is
the right shape, and the 4 KiB it admits is a bounded, documented 69 ms rather
than an unbounded anything. The numbers are recorded so the next person to
consider raising it does so with the exponent in front of them.

Encode is 3.1× decode at every size, and that is not obvious: encode divides a
magnitude down by the radix (a real integer division per byte), decode
multiplies one up (a multiply plus a division by 256 the compiler turns into a
shift).

> **Hypothesis tested and refuted.** If the encode's long division were bound
> by the divisor being a runtime value, giving the compiler a compile-time
> radix would strength-reduce it. Measured side by side on the same 512-byte
> magnitude: runtime radix **2 168 ns**, constant radix **2 143 ns**, and a
> hand-split `q := acc/radix; rem := acc - q*radix` **2 173 ns**. All within
> 1.4 %. The division is not the bound and Go already fuses `DIV`/`MOD`. Not
> pursued further: shaving the constant would not move a decision that sits
> inside a three-order-of-magnitude gap.

## 3. The decode allocated once per output byte — 2 049 allocations for 1 KiB

`BenchmarkDecode/base58` originally reported **1 604 834 ns, 558 378 B,
2 049 allocs**. A memory profile named the source with no ambiguity:

| source | share of allocated objects |
|---|---:|
| `prependByte`, inlined into `mulAddInPlace` | **97.85 %** |
| `decodeBaseN` itself | 1.14 % |
| `assembleDecoded` | 0.85 % |

The accumulator was big-endian and grew at the *front*, so each new
most-significant byte ran `append([]byte{v}, b...)` — a fresh allocation **and**
a full copy of the magnitude, once per byte of output. The CPU profile agreed:
`growslice` 14.77 % cumulative, `mallocgc` 8.90 %.

Nothing about the format requires that. The magnitude of an n-digit
base-radix number is below 256^n for every radix under 256, so `len(s)` bytes
always hold it: `decodeBaseN` now allocates that buffer **once** and
`mulAddWindow` grows the live window leftwards into the reserved prefix by
decrementing an index.

| `Decode/base58`, 1 KiB | before | after |
|---|---:|---:|
| ns/op | 1 604 834 | **1 384 209** |
| B/op | 558 378 | **2 432** |
| allocs/op | 2 049 | **2** |

**230× fewer bytes, 1 024× fewer allocations**, and the asymptotic behaviour
is unchanged — this removed the allocator from a quadratic loop, it did not
make the loop linear. Every test stayed green; `Test_prependByte` and
`Test_mulAddInPlace` were replaced by `Test_mulAddWindow`, which additionally
pins that the unconsumed reserve stays zero.

## 4. `base16` is not `hex`, and the difference was a second pass

Same alphabet, different case, and `base16` cost **3.13×** `hex`. The stdlib
ships no uppercase hex encoder, so `base16` called `hex.Encode` and then walked
the output again fixing case. Two things were wrong with that, and a controlled
A/B in one process on 1 KiB separates them:

| shape | ns/op | vs stdlib lowercase |
|---|---:|---:|
| A — `hex.Encode` + branchy case pass (**shipped**) | 4 949 | 3.13× |
| B — `hex.Encode` + branchless case pass | 3 078 | 1.95× |
| C — single pass from an uppercase table (**now**) | **1 449** | **0.92×** |
| D — `hex.Encode` alone, the floor | 1 580 | 1.00× |

**A → B is the branch.** On hex output the letters are 6 of 16 symbols and
their positions are the input's own bit pattern, so `if b >= 'a' && b <= 'f'`
has nothing to learn. The same branchy loop on all-zero input — where the
branch is never taken and always predicted — cost **2 877 ns** against
**4 853 ns** on pseudo-random input, while the branchless form measured
2 933 ns and 3 061 ns on the same two inputs. The branchy version's cost
depended on the *content* of what it encoded; the branchless one does not.

**B → C is the pass itself.** Emitting `"0123456789ABCDEF"[b>>4]` directly
makes the second walk not exist. `base16` now costs what `hex` costs, which is
what it should always have cost — the whole difference between them is which
sixteen bytes get indexed.

At 64 KiB the package benchmark shows 143 998 ns for `base16` against
123 413 ns for `hex`; the residual is inside this run's noise band and the
isolated A/B above is the load-bearing measurement.

## 5. Left alone, with the reason

- **The encode side of `base58`/`base62`** allocates 3 and stays there: the
  working magnitude, the digit accumulator, and the returned slice. The digit
  accumulator is pre-sized at `len(raw)*2` and the returned slice exactly. The
  time is `divmodInPlace` (23.24 % flat of the whole encode profile) and that
  is the algorithm, not the plumbing — see the refuted hypothesis in §2.
- **`ascii85` decode** is 6 allocations. It is the only variant with no
  `AppendDecode` in the stdlib, so it goes through a `bytes.Buffer`. It is also
  the most compact encoding here, which is the trade it exists to offer.
- **Every block encode is 1 allocation**, and it is the returned slice. There
  is no pool to add: the caller owns the result.

## Reproducibility envelope

> **Numbers vary across machines, and this run shared the box.** Two other
> agent jobs were active throughout; load average ranged 2.7 to 6.1 on 8 cores.
> Every figure below is the **median of three `-benchtime=1s` runs**, and the
> run-to-run spread is reported here rather than hidden: it was under 5 % for
> 42 of the 60 sub-benchmarks and under 1 % for all six base-conversion rows
> (they are long enough to swamp scheduler noise), but reached 33 % on
> `EncodeLarge/base32` and 17 % on the two 1 KiB hex rows. **The ratios and the
> allocation counts are what this report asserts**; the allocation columns are
> invariant and did not vary at all across runs.
>
> The two before/after comparisons in §3 and §4 are deliberately **not** taken
> from separate package runs. §3's allocation counts are exact and
> load-independent, and §4 is a three-way A/B inside a single process on one
> payload.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| CPU                | AMD EPYC 7351P 16-Core Processor |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `agent-ae8135b496985ac91` (off `jaimerias-que-tu-te-connect`) |
| Git commit         | `c30e2ad` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-count=3`, medians, machine under load |

## Results

Median run of three per row, verbatim.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/codec/baseenc
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkEncode/base16-8     	  462918	      2647 ns/op	         2.000 x-expand	    2048 B/op	       1 allocs/op
BenchmarkEncode/hex-8        	  509868	      2139 ns/op	         2.000 x-expand	    2048 B/op	       1 allocs/op
BenchmarkEncode/base32-8     	  650323	      1552 ns/op	         1.602 x-expand	    1792 B/op	       1 allocs/op
BenchmarkEncode/base45-8     	  419888	      2727 ns/op	         1.500 x-expand	    1792 B/op	       1 allocs/op
BenchmarkEncode/base58-8     	     268	   4337669 ns/op	         1.365 x-expand	    4480 B/op	       3 allocs/op
BenchmarkEncode/base62-8     	     279	   4306150 ns/op	         1.344 x-expand	    4480 B/op	       3 allocs/op
BenchmarkEncode/base64-8     	  685758	      1557 ns/op	         1.336 x-expand	    1408 B/op	       1 allocs/op
BenchmarkEncode/base64url-8  	  747853	      1577 ns/op	         1.336 x-expand	    1408 B/op	       1 allocs/op
BenchmarkEncode/ascii85-8    	  322400	      3182 ns/op	         1.250 x-expand	    1280 B/op	       1 allocs/op
BenchmarkDecode/base16-8     	  758958	      1505 ns/op	    1024 B/op	       1 allocs/op
BenchmarkDecode/hex-8        	  775128	      1507 ns/op	    1024 B/op	       1 allocs/op
BenchmarkDecode/base32-8     	  201620	      5902 ns/op	    2816 B/op	       2 allocs/op
BenchmarkDecode/base45-8     	  621022	      1850 ns/op	    1152 B/op	       1 allocs/op
BenchmarkDecode/base58-8     	     842	   1384209 ns/op	    2432 B/op	       2 allocs/op
BenchmarkDecode/base62-8     	     871	   1365026 ns/op	    2432 B/op	       2 allocs/op
BenchmarkDecode/base64-8     	  647940	      1814 ns/op	    1024 B/op	       1 allocs/op
BenchmarkDecode/base64url-8  	  632094	      1808 ns/op	    1024 B/op	       1 allocs/op
BenchmarkDecode/ascii85-8    	  271513	      4406 ns/op	    4528 B/op	       6 allocs/op
BenchmarkEncodeScaling/base64/64-8      	 8226430	       145.0 ns/op	      96 B/op	       1 allocs/op
BenchmarkEncodeScaling/base64/256-8     	 2762602	       451.4 ns/op	     352 B/op	       1 allocs/op
BenchmarkEncodeScaling/base64/1024-8    	  693061	      1573 ns/op	    1408 B/op	       1 allocs/op
BenchmarkEncodeScaling/base64/4096-8    	  182965	      6541 ns/op	    6144 B/op	       1 allocs/op
BenchmarkEncodeScaling/base58/64-8      	   69052	     17525 ns/op	     288 B/op	       3 allocs/op
BenchmarkEncodeScaling/base58/256-8     	    4376	    272421 ns/op	    1120 B/op	       3 allocs/op
BenchmarkEncodeScaling/base58/1024-8    	     258	   4362386 ns/op	    4480 B/op	       3 allocs/op
BenchmarkEncodeScaling/base58/4096-8    	      16	  69297872 ns/op	   18432 B/op	       3 allocs/op
BenchmarkDecodeScaling/base64/64-8      	 5740010	       215.0 ns/op	      64 B/op	       1 allocs/op
BenchmarkDecodeScaling/base64/256-8     	 1779448	       698.3 ns/op	     256 B/op	       1 allocs/op
BenchmarkDecodeScaling/base64/1024-8    	  520082	      2228 ns/op	    1024 B/op	       1 allocs/op
BenchmarkDecodeScaling/base64/4096-8    	  143430	      8706 ns/op	    4096 B/op	       1 allocs/op
BenchmarkDecodeScaling/base58/64-8      	  203122	      5747 ns/op	     160 B/op	       2 allocs/op
BenchmarkDecodeScaling/base58/256-8     	   13638	     88034 ns/op	     608 B/op	       2 allocs/op
BenchmarkDecodeScaling/base58/1024-8    	     866	   1393386 ns/op	    2432 B/op	       2 allocs/op
BenchmarkDecodeScaling/base58/4096-8    	      54	  22286273 ns/op	   10240 B/op	       2 allocs/op
BenchmarkEncodeLarge/base16-8           	    8372	    143998 ns/op	  131073 B/op	       1 allocs/op
BenchmarkEncodeLarge/hex-8              	    9844	    123413 ns/op	  131073 B/op	       1 allocs/op
BenchmarkEncodeLarge/base32-8           	   10000	    106415 ns/op	  106496 B/op	       1 allocs/op
BenchmarkEncodeLarge/base45-8           	    6549	    212298 ns/op	  106497 B/op	       1 allocs/op
BenchmarkEncodeLarge/base64-8           	    9249	    136278 ns/op	   90113 B/op	       1 allocs/op
BenchmarkEncodeLarge/base64url-8        	    9862	    127561 ns/op	   90112 B/op	       1 allocs/op
BenchmarkEncodeLarge/ascii85-8          	    5350	    223337 ns/op	   81920 B/op	       1 allocs/op
BenchmarkMarshal/base16-8               	  175410	      6640 ns/op	    3124 B/op	       3 allocs/op
BenchmarkMarshal/hex-8                  	  235333	      6023 ns/op	    3123 B/op	       3 allocs/op
BenchmarkMarshal/base32-8               	  250178	      4691 ns/op	    2354 B/op	       3 allocs/op
BenchmarkMarshal/base45-8               	  198306	      6099 ns/op	    2354 B/op	       3 allocs/op
BenchmarkMarshal/base58-8               	     153	   7856763 ns/op	    6635 B/op	       5 allocs/op
BenchmarkMarshal/base62-8               	     156	   7685606 ns/op	    6634 B/op	       5 allocs/op
BenchmarkMarshal/base64-8               	  290640	      4043 ns/op	    2098 B/op	       3 allocs/op
BenchmarkMarshal/base64url-8            	  298623	      4173 ns/op	    2098 B/op	       3 allocs/op
BenchmarkMarshal/ascii85-8              	  169106	      6456 ns/op	    1842 B/op	       3 allocs/op
BenchmarkUnmarshal/base16-8             	  177038	      7793 ns/op	    2481 B/op	       4 allocs/op
BenchmarkUnmarshal/hex-8                	  180086	      6799 ns/op	    2480 B/op	       4 allocs/op
BenchmarkUnmarshal/base32-8             	   90296	     13264 ns/op	    4786 B/op	       5 allocs/op
BenchmarkUnmarshal/base45-8             	  157308	      7585 ns/op	    2481 B/op	       4 allocs/op
BenchmarkUnmarshal/base58-8             	     483	   2493983 ns/op	    4531 B/op	       5 allocs/op
BenchmarkUnmarshal/base62-8             	     489	   2446151 ns/op	    4531 B/op	       5 allocs/op
BenchmarkUnmarshal/base64-8             	  163068	      8673 ns/op	    2481 B/op	       4 allocs/op
BenchmarkUnmarshal/base64url-8          	  152077	      8439 ns/op	    2481 B/op	       4 allocs/op
BenchmarkUnmarshal/ascii85-8            	   79800	     14619 ns/op	    6563 B/op	      10 allocs/op
BenchmarkMarshalJSONStep-8              	  807127	      1597 ns/op	      48 B/op	       2 allocs/op
```
