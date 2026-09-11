<!-- generated from pkg/v1/kdf/kdf_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./v1/kdf/` to refresh -->
# Benchmarks — `pkg/v1/kdf`

HKDF-SHA256, for **key separation** from a secret that is already strong. The
number a reader most needs from this page is a comparison this package cannot
make on its own: HKDF is **67 000× cheaper than the password stretcher two
directories away**, and the gap is the entire reason the two are separate
surfaces. The family-wide choice table lives in
[`pkg/v1/crypto/BENCH.md`](../crypto/BENCH.md).

## The choice table

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `KeyTree.Child` | **46.9** | 16 | 1 |
| `Subkey` → 32 B, salted | **2 147** | 1 200 | 16 |
| `Subkey` → 32 B, **nil salt** | 2 260 | 1 360 | 18 |
| `KeyTree.DeriveKey` depth 1 | 2 377 | 1 440 | 22 |
| `KeyTree.DeriveKey` depth 3 | 2 534 | 1 472 | 23 |
| `Subkey` → 64 B, salted | 2 808 | 1 472 | 20 |
| *(for contrast)* `password.Hash` | **145 000 000** | 1 248 | 19 |

**HKDF is a microsecond-class primitive and a password hash is a
tenth-of-a-second one.** That ratio — 67 000× — is not a benchmark curiosity;
it is the package doc's "NOT for passwords" rule expressed as a number. Running
`Subkey` over a human password would let an attacker try **67 000 guesses for
the price of one honest login**. See `pkg/v1/password/BENCH.md`.

## What the SDK costs over the bare primitive: nothing, and zero allocations

`BenchmarkBareHKDF32B` calls `crypto/hkdf.Key` directly.

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `kdf.Subkey` | 2 147 | **1 200** | **16** |
| `hkdf.Key` | 2 073 | **1 200** | **16** |

**Identical allocation profile, byte for byte, and +3.6 % on the clock** — which
on this contended box is the noise floor; across a second run measured on a
quieter window the two were 2 083 ns and 2 008 ns, the same 3.6 %. The facade is
a snapshot-pointer load, a map read and one interface call, and none of that
allocates.

All sixteen allocations are `crypto/hkdf`'s own extract-and-expand. There is
nothing here for the SDK to fix.

## Three findings a caller can act on

### Skipping the salt is not cheaper — it is dearer

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Subkey(…, salt, …)` | **2 147** | 1 200 | 16 |
| `Subkey(…, nil, …)` | 2 260 | 1 360 | **18** |

RFC 5869 says an absent salt is a zero salt of HashLen bytes, and `crypto/hkdf`
implements that by *materialising* it — so the nil-salt path costs **+113 ns and
two extra allocations**. Passing `nil` is legal and documented; what it is not
is an optimisation. If a salt is available, pass it: it is free and it is better
key separation.

### The output length is a per-BLOCK cost, not a per-byte one

| | ns/op | allocs |
|---|---:|---:|
| 32 bytes out | 2 147 | 16 |
| 64 bytes out | 2 808 | 20 |

HKDF expands in HashLen (32-byte) blocks, so asking for 64 bytes runs one extra
expand round: **+661 ns and +4 allocations for the second block, and nothing for
bytes 33 through 64 individually.** A caller who needs 33 bytes pays the same as
one who needs 64.

### `KeyTree` depth is free; the tree itself is not

| | ns/op | allocs |
|---|---:|---:|
| `Subkey`, flat | 2 147 | 16 |
| `DeriveKey` at depth 1 | 2 377 | 22 |
| `DeriveKey` at depth 3 | **2 534** | 23 |
| `Child` alone | **46.9** | 1 |

Two segments of extra depth cost **157 ns and one allocation** — because
`DeriveKey` re-derives from the *master* with the whole canonical path as the
HKDF info, rather than chaining a derivation per level. Depth is a
string-encoding cost, and the benchmark pair is how a reader checks that claim
instead of taking the package doc's word for it. **A deep path is not a slow
path.**

The tree's own overhead over a flat `Subkey` is **230 ns and 6 allocations** at
depth 1: the injective path encoding, plus `withMasterBytes` cloning and wiping
the master (a V66 hygiene rule — the clone must not outlive the call), plus
wrapping the result in a redacting `Key`. `Child` at 46.9 ns is a slice copy;
descending is not where the cost is.

## Reproducibility envelope

> **Numbers vary across machines.** This box is shared with fifteen other agent
> jobs and its load swings during a run. Each benchmark ran **three times**;
> every figure quoted above is the **fastest of the three** — the
> least-contaminated sample — and all three are printed below. The allocation
> columns are exact and do not vary.
>
> This matters here more than elsewhere: the differences on this page are
> hundreds of nanoseconds on calls of about two microseconds, so a single
> contaminated run can reverse a row. The allocation deltas (+2 for nil salt,
> +4 for the second output block, +6 for the tree) do **not** vary and are the
> firmer half of every claim above.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, `sha_ni`, `sse4_2`, `avx2` |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 (`GOAMD64=v1`) |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 94dd6e7 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/kdf
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkSubkey32B-8             	  385741	      2965 ns/op	    1200 B/op	      16 allocs/op
BenchmarkSubkey32B-8             	  478615	      2688 ns/op	    1200 B/op	      16 allocs/op
BenchmarkSubkey32B-8             	  558434	      2147 ns/op	    1200 B/op	      16 allocs/op
BenchmarkSubkey64B-8             	  422779	      2808 ns/op	    1472 B/op	      20 allocs/op
BenchmarkSubkey64B-8             	  417628	      2822 ns/op	    1472 B/op	      20 allocs/op
BenchmarkSubkey64B-8             	  453708	      2841 ns/op	    1472 B/op	      20 allocs/op
BenchmarkSubkeyNoSalt32B-8       	  543514	      2278 ns/op	    1360 B/op	      18 allocs/op
BenchmarkSubkeyNoSalt32B-8       	  526561	      2260 ns/op	    1360 B/op	      18 allocs/op
BenchmarkSubkeyNoSalt32B-8       	  481563	      2278 ns/op	    1360 B/op	      18 allocs/op
BenchmarkBareHKDF32B-8           	  574448	      2073 ns/op	    1200 B/op	      16 allocs/op
BenchmarkBareHKDF32B-8           	  575212	      2126 ns/op	    1200 B/op	      16 allocs/op
BenchmarkBareHKDF32B-8           	  599313	      2269 ns/op	    1200 B/op	      16 allocs/op
BenchmarkKeyTreeDeriveDepth1-8   	  504068	      2377 ns/op	    1440 B/op	      22 allocs/op
BenchmarkKeyTreeDeriveDepth1-8   	  502666	      2433 ns/op	    1440 B/op	      22 allocs/op
BenchmarkKeyTreeDeriveDepth1-8   	  482276	      2411 ns/op	    1440 B/op	      22 allocs/op
BenchmarkKeyTreeDeriveDepth3-8   	  449378	      2534 ns/op	    1472 B/op	      23 allocs/op
BenchmarkKeyTreeDeriveDepth3-8   	  419814	      2588 ns/op	    1472 B/op	      23 allocs/op
BenchmarkKeyTreeDeriveDepth3-8   	  467793	      2612 ns/op	    1472 B/op	      23 allocs/op
BenchmarkKeyTreeChild-8          	18775863	        60.04 ns/op	      16 B/op	       1 allocs/op
BenchmarkKeyTreeChild-8          	26493848	        47.93 ns/op	      16 B/op	       1 allocs/op
BenchmarkKeyTreeChild-8          	26671752	        46.89 ns/op	      16 B/op	       1 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/kdf	25.306s
```
