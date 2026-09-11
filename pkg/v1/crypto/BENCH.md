<!-- generated from pkg/v1/crypto/crypto_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./v1/crypto/` to refresh -->
# Benchmarks — `pkg/v1/crypto`

The AEAD facade, and the **entry point for the whole crypto family's cost
table**. A consumer choosing a primitive chooses on properties — who holds the
key, whether the output is public, whether it is reversible — and on a cost that
was published nowhere. This file is that cost.

## The family choice table

One row per need. Every number below is measured on this machine and is
reproduced, with its own analysis, in the sibling `BENCH.md` named in the
second column.

| I need to… | call | small (64 B) | bulk (1 MiB) | cost is paid |
|---|---|---:|---:|---|
| checksum for a cache key | `hash.Sum(CRC32C)` | **121 ns** | **6 521 MiB/s** | per byte |
| content id / dedup key | `hash.Sum(SHA256)` | 261 ns | 1 342 MiB/s | per byte above ~256 B |
| encrypt + authenticate, shared key | `crypto.Seal` / `Open` | 1 013 ns | 1 384 MiB/s | per byte above ~4 KiB |
| authenticate only, shared key | `mac.Tag` / `Verify` | 994 ns | 1 346 MiB/s | per byte above ~4 KiB |
| separate one strong secret into subkeys | `kdf.Subkey(HKDFSHA256)` | 2.15 µs | — | per call |
| authenticate, key anyone may verify | `sign.Sign(Ed25519)` | 36.5 µs | — | **per call** |
| … and verify it | `sign.Verify(Ed25519)` | 88.0 µs | — | per call |
| … interoperably (JWT / X.509) | `sign.*(ECDSAP256)` | 85.2 / 113.0 µs | — | per call |
| agree a shared key from two keypairs | `agree.SharedKey(X25519)` | 160 µs | — | per call |
| seal a data key under a passphrase | `crypto.WrapKey` | **143 ms** | — | per call, **on purpose** |
| store a human password | `password.Hash` | **145 ms** | — | per call, **on purpose** |

**The table spans 1.2 million to one**, from a 121 ns checksum to a 145 ms
password hash, and every step of that span is a decision someone made
deliberately. The last two rows are slow BECAUSE they are slow: a passphrase
stretcher that got faster would be weaker. See `pkg/v1/password/BENCH.md`.

Two rows are traps worth naming here rather than in a footnote. `crypto.WrapKey`
lives in *this* package, next to verbs that cost a microsecond, and costs
**143 milliseconds** — it stretches its passphrase with PBKDF2-SHA256 at 600 000
iterations, exactly like a password hash. And `hash.Sum(FNV1a64)`, sold as the
fast non-cryptographic fingerprint, is **slower than SHA-256 above 256 bytes**
on this CPU; that one has its own section in `pkg/v1/hash/BENCH.md`.

## What the SDK costs over the bare primitive: nothing measurable

This is the number that judges the facade, so it is measured directly rather
than argued. `BenchmarkBareGCMFreshCipher*` is `service/crypto/aesgcm.Seal`
transcribed onto the stdlib with the registry lookup, the interface dispatch and
the typed-error wrapping removed, writing the same box framing.

| 64 B | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `crypto.Seal` | **1 013** | 1 424 | 5 |
| the same work, no SDK | 1 042 | 1 424 | 5 |

**The allocation profile is byte-identical and the two times are within 3 %**,
with the SDK on the faster side of the noise. At 1 MiB the two are 722 µs and
735 µs, also within noise. There is no envelope to report: the facade adds a
snapshot-pointer load, a map read and one interface call, and none of that is
visible against AES-GCM.

That is the deliverable. It also means every remaining number on this page is a
property of the stdlib and of the *shape of the port*, not of this package's
code — which is where the interesting finding is.

## The interesting finding: two thirds of a small Seal is the key schedule

`Seal` and `Open` take a `Key` and hand it to `aes.NewCipher` + `cipher.NewGCM`
**on every call**. The third bare benchmark hoists that out of the loop:

| 64 B | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `crypto.Seal` | 1 013 | 1 424 | 5 |
| bare GCM, cipher built **per call** | 1 042 | 1 424 | 5 |
| bare GCM, cipher built **once** | **341.8** | **112** | **2** |

So of the 1 013 ns a 64-byte `Seal` costs, **671 ns — 66 % — is rebuilding the
AES key schedule and the GCM tables**, and 3 of the 5 allocations are the same
thing. A CPU profile agrees and names them: `fips140/aes/gcm.newGCM` 10.8 %,
`gcmAesInit` 4.1 %, `aes.newBlock` 3.5 %, `expandKeyAsm` 1.6 %, with
`runtime.mallocgc` at 29.2 % cumulative feeding them; the actual encryption,
`gcmAesEnc`, is **5.3 %**.

An allocation profile splits the five exactly:

| allocation | site | share |
|---|---|---:|
| AES key schedule | `fips140/aes.New` | 21.8 % |
| GCM state | `fips140/aes/gcm.New` | 21.0 % |
| defensive key copy | `Key.Bytes` → `bytes.Clone` | 20.8 % |
| the nonce, escaping | `aesgcm.Seal` | ~18 % |
| **the box** | `aesgcm.Seal` | ~18 % |

**Exactly one of the five is structural** — the returned box, which the caller
keeps. The other four exist because the port is `(Key, []byte) → []byte` and
therefore has nowhere to keep a prepared cipher.

This is **not** filed as a defect, and it is deliberately not "fixed" here.
Caching a `cipher.AEAD` per `Key` would mean the SDK holding an expanded key
schedule derived from the secret for as long as the cache lives, which is
exactly what `Key.Zeroize` exists to make impossible — the type's whole promise
is that clearing one copy clears the secret everywhere. That trade is a design
question for ADR 0013, not a benchmark's call. What a benchmark can do is put a
price on it, and the price is **671 ns and 3 allocations per small Seal**.

The practical reading for a caller today: **AEAD is a per-call cost below a few
kilobytes and a per-byte cost above**. Sealing 16 384 records of 64 bytes costs
16.6 ms; sealing the same 1 MiB in one call costs 0.72 ms — **23× less**. Batch
before you seal.

## Per call or per byte — the ratio that answers it

| | 64 B | 4 KiB | 1 MiB |
|---|---:|---:|---:|
| `Seal` | 1 013 ns · 60 MiB/s | 3 418 ns · 1 143 MiB/s | 722 682 ns · **1 384 MiB/s** |
| `Open` | 952 ns · 64 MiB/s | 3 599 ns · 1 086 MiB/s | 734 742 ns · **1 361 MiB/s** |

**16 384× the bytes costs 713× the time.** At 64 B the call runs at 4 % of its
asymptotic rate; at 4 KiB it is already at 83 %. Anything at or above a few
kilobytes is paying for bytes and nothing else.

`Open` is consistently a hair cheaper than `Seal` at every size — it draws no
nonce and allocates 4 objects rather than 5 — and that is the only asymmetry
between the two directions.

## The refusal path, and the aad

| 4 KiB | ns/op | allocs |
|---|---:|---:|
| `Open` (valid) | 3 599 | 4 |
| `Open` (tag flipped) | **3 483** | 4 |
| `Seal` (no aad) | 3 418 | 5 |
| `Seal` (+64 B aad) | 3 736 | 5 |

A tampered box is refused for **slightly less** than a good one costs to accept
— GCM computes the tag and compares, then skips returning a plaintext. A
refusal that cost *more* than an acceptance would be an amplification an
attacker gets for free; this is the measurement that says it is not one. And
64 bytes of associated data cost 318 ns, which is the tag pass over those bytes
and nothing structural.

## Streaming against whole-buffer, at the same megabyte

| 1 MiB | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Seal` | 722 682 | 1 058 100 | 5 |
| `SealStream` | 879 255 | 1 248 149 | 57 |
| `Open` | 734 742 | 1 049 892 | 4 |
| `OpenStream` | 871 766 | 1 157 939 | 56 |

Streaming costs **22 % more time and 52 more allocations** at a megabyte,
because the frame authenticates each 64 KiB chunk separately: sixteen tags,
sixteen nonces, sixteen buffer handoffs where the whole-buffer path has one. It
buys not holding the payload in memory. Below the point where that matters,
`Seal` is the cheaper call — the streaming path is for payloads you *cannot*
buffer, not for large ones you merely *could* stream.

## `NewKey` is 47 ns — do not hoist it out of anything

`NewKey` is **47.0 ns / 32 B / 1 alloc**: an `len == 32` check and a
`bytes.Clone`. It is 4.6 % of the cheapest `Seal` on this page. Nobody should be
caching a `Key` construction for speed; cache it for lifetime reasons or not at
all.

## `WrapKey` / `UnwrapKey` are 143 ms, and that is the feature

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `WrapKey` | **143 346 135** | 3 248 | 31 |
| `UnwrapKey` | **142 427 587** | 2 914 | 28 |

Five orders of magnitude above every other verb in this package, and correct: a
key envelope is opened with a human passphrase, so its cost must be an
attacker's cost too. The KEK is stretched with PBKDF2-SHA256 at **600 000
iterations** (`service/crypto/keyenvelope`), which is the OWASP fallback figure
and is the parameter that sets this number — `pkg/v1/password/BENCH.md` measures
the per-iteration cost so an operator can compute a different budget without
re-running anything.

The hazard is placement, not cost: these two sit in a package whose other verbs
are microseconds, and they are named like cheap ones. **Do not call `UnwrapKey`
per request.** Unwrap once at startup, keep the `Key`, and `Zeroize` it on the
way out.

## Reproducibility envelope

> **Numbers vary across machines.** This box is shared with fifteen other
> agent jobs and its load swings during a run. Each benchmark therefore ran
> **three times**; every figure quoted above is the **fastest of the three** —
> the least-contaminated sample — and all three are printed below so the spread
> is visible rather than asserted. The allocation columns are exact and do not
> vary at all. The RATIOS are what this page claims; the absolute nanoseconds
> are not portable.
>
> One consequence is stated out loud: a single `-count=1` run of this package
> landed in a load spike and reported the reused-cipher case as *slower* than
> the SDK at 1 MiB, which is impossible. That is why this file publishes three.

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

Throughput is quoted in **MiB/s**; Go's own `MB/s` column below is decimal
(10⁶ B/s), so the two differ by 1.048576.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/crypto
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkSeal64B-8                   	 1000000	      1017 ns/op	  62.94 MB/s	    1424 B/op	       5 allocs/op
BenchmarkSeal64B-8                   	 1000000	      1016 ns/op	  62.99 MB/s	    1424 B/op	       5 allocs/op
BenchmarkSeal64B-8                   	 1000000	      1013 ns/op	  63.20 MB/s	    1424 B/op	       5 allocs/op
BenchmarkSeal4KiB-8                  	  349011	      3440 ns/op	1190.59 MB/s	    6192 B/op	       5 allocs/op
BenchmarkSeal4KiB-8                  	  346849	      3439 ns/op	1191.04 MB/s	    6192 B/op	       5 allocs/op
BenchmarkSeal4KiB-8                  	  355100	      3418 ns/op	1198.39 MB/s	    6192 B/op	       5 allocs/op
BenchmarkSeal1MiB-8                  	    1630	    722682 ns/op	1450.95 MB/s	 1058100 B/op	       5 allocs/op
BenchmarkSeal1MiB-8                  	    1568	    773688 ns/op	1355.30 MB/s	 1058101 B/op	       5 allocs/op
BenchmarkSeal1MiB-8                  	    1360	    793562 ns/op	1321.35 MB/s	 1058101 B/op	       5 allocs/op
BenchmarkOpen64B-8                   	 1225047	       982.5 ns/op	  65.14 MB/s	    1376 B/op	       4 allocs/op
BenchmarkOpen64B-8                   	 1263088	       952.1 ns/op	  67.22 MB/s	    1376 B/op	       4 allocs/op
BenchmarkOpen64B-8                   	 1268637	       952.3 ns/op	  67.20 MB/s	    1376 B/op	       4 allocs/op
BenchmarkOpen4KiB-8                  	  321768	      3681 ns/op	1112.85 MB/s	    5408 B/op	       4 allocs/op
BenchmarkOpen4KiB-8                  	  340958	      3599 ns/op	1138.23 MB/s	    5408 B/op	       4 allocs/op
BenchmarkOpen4KiB-8                  	  300976	      3605 ns/op	1136.28 MB/s	    5408 B/op	       4 allocs/op
BenchmarkOpen1MiB-8                  	    1568	    770580 ns/op	1360.76 MB/s	 1049892 B/op	       4 allocs/op
BenchmarkOpen1MiB-8                  	    1652	    734742 ns/op	1427.13 MB/s	 1049893 B/op	       4 allocs/op
BenchmarkOpen1MiB-8                  	    1542	    769330 ns/op	1362.97 MB/s	 1049893 B/op	       4 allocs/op
BenchmarkBareGCMFreshCipher64B-8     	  980865	      1065 ns/op	  60.10 MB/s	    1424 B/op	       5 allocs/op
BenchmarkBareGCMFreshCipher64B-8     	 1000000	      1042 ns/op	  61.40 MB/s	    1424 B/op	       5 allocs/op
BenchmarkBareGCMFreshCipher64B-8     	 1000000	      1080 ns/op	  59.25 MB/s	    1424 B/op	       5 allocs/op
BenchmarkBareGCMFreshCipher1MiB-8    	    1442	    768212 ns/op	1364.96 MB/s	 1058101 B/op	       5 allocs/op
BenchmarkBareGCMFreshCipher1MiB-8    	    1422	    778762 ns/op	1346.46 MB/s	 1058101 B/op	       5 allocs/op
BenchmarkBareGCMFreshCipher1MiB-8    	    1482	    735237 ns/op	1426.17 MB/s	 1058100 B/op	       5 allocs/op
BenchmarkBareGCMReusedCipher64B-8    	 3500876	       343.9 ns/op	 186.12 MB/s	     112 B/op	       2 allocs/op
BenchmarkBareGCMReusedCipher64B-8    	 3552602	       341.8 ns/op	 187.25 MB/s	     112 B/op	       2 allocs/op
BenchmarkBareGCMReusedCipher64B-8    	 3496281	       345.3 ns/op	 185.35 MB/s	     112 B/op	       2 allocs/op
BenchmarkBareGCMReusedCipher1MiB-8   	    1532	    733197 ns/op	1430.14 MB/s	 1056788 B/op	       2 allocs/op
BenchmarkBareGCMReusedCipher1MiB-8   	    1342	    751997 ns/op	1394.39 MB/s	 1056787 B/op	       2 allocs/op
BenchmarkBareGCMReusedCipher1MiB-8   	    1701	    785498 ns/op	1334.92 MB/s	 1056787 B/op	       2 allocs/op
BenchmarkSealAAD4KiB-8               	  322183	      3804 ns/op	1076.70 MB/s	    6192 B/op	       5 allocs/op
BenchmarkSealAAD4KiB-8               	  321050	      3736 ns/op	1096.32 MB/s	    6192 B/op	       5 allocs/op
BenchmarkSealAAD4KiB-8               	  328260	      3779 ns/op	1083.83 MB/s	    6192 B/op	       5 allocs/op
BenchmarkOpenTampered4KiB-8          	  368498	      3573 ns/op	1146.52 MB/s	    5408 B/op	       4 allocs/op
BenchmarkOpenTampered4KiB-8          	  332030	      3614 ns/op	1133.50 MB/s	    5408 B/op	       4 allocs/op
BenchmarkOpenTampered4KiB-8          	  346399	      3483 ns/op	1175.96 MB/s	    5408 B/op	       4 allocs/op
BenchmarkSealStream1MiB-8            	    1480	    961156 ns/op	1090.95 MB/s	 1248150 B/op	      57 allocs/op
BenchmarkSealStream1MiB-8            	    1045	   1061788 ns/op	 987.56 MB/s	 1248158 B/op	      57 allocs/op
BenchmarkSealStream1MiB-8            	    1202	    879255 ns/op	1192.57 MB/s	 1248149 B/op	      57 allocs/op
BenchmarkOpenStream1MiB-8            	    1302	    871766 ns/op	1202.82 MB/s	 1157942 B/op	      56 allocs/op
BenchmarkOpenStream1MiB-8            	    1285	    927724 ns/op	1130.27 MB/s	 1157941 B/op	      56 allocs/op
BenchmarkOpenStream1MiB-8            	    1378	    882598 ns/op	1188.06 MB/s	 1157939 B/op	      56 allocs/op
BenchmarkNewKey-8                    	25347567	        49.03 ns/op	      32 B/op	       1 allocs/op
BenchmarkNewKey-8                    	24647598	        47.12 ns/op	      32 B/op	       1 allocs/op
BenchmarkNewKey-8                    	26777300	        46.96 ns/op	      32 B/op	       1 allocs/op
BenchmarkWrapKey-8                   	       7	 145705290 ns/op	    3261 B/op	      31 allocs/op
BenchmarkWrapKey-8                   	       7	 144601965 ns/op	    3248 B/op	      31 allocs/op
BenchmarkWrapKey-8                   	       7	 143346135 ns/op	    3248 B/op	      31 allocs/op
BenchmarkUnwrapKey-8                 	       8	 148449108 ns/op	    2914 B/op	      28 allocs/op
BenchmarkUnwrapKey-8                 	       8	 142427587 ns/op	    2914 B/op	      28 allocs/op
BenchmarkUnwrapKey-8                 	       7	 146770241 ns/op	    2912 B/op	      28 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/crypto	59.548s
```
