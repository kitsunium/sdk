<!-- generated from pkg/v1/sign/sign_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./v1/sign/` to refresh -->
# Benchmarks — `pkg/v1/sign`

Two signature schemes. A signature is produced **once** and verified by
**everyone**, so the two sides of a scheme have very different budgets and the
asymmetry between them decides architectures. That asymmetry is the subject of
this page. The family-wide choice table lives in
[`pkg/v1/crypto/BENCH.md`](../crypto/BENCH.md).

## The choice table

| scheme | sign | verify | **verify / sign** | sign allocs | verify allocs |
|---|---:|---:|---:|---:|---:|
| **Ed25519** | **36.5 µs** | **88.0 µs** | **2.41×** | **1** | **0** |
| ECDSA P-256 | 85.2 µs | 113.0 µs | 1.33× | 90 | 20 |

**Ed25519 wins on both sides** — 2.3× cheaper to sign, 1.3× cheaper to verify —
and it wins on allocations by two orders of magnitude, verifying with **zero**.
Choose ECDSA P-256 only when the far side demands it (JWT, X.509, an existing
PKI); it is the interoperability tax, and this table is its size.

The message length is not an axis. A signature is taken over a digest, so
4 KiB costs 58.1 µs to sign against 64 B's 36.5 µs for Ed25519 — the 22 µs
difference is one SHA-512 pass over the extra bytes, not a scheme property.

## The headline: verification costs MORE than signing in BOTH schemes

The received wisdom is that ECDSA verifies dearer than it signs while Ed25519
does the opposite. **Half of that is wrong here, and the measurement says so:**

| | sign | verify | ratio |
|---|---:|---:|---:|
| Ed25519 | 36 498 ns | 88 044 ns | **2.41× — verify is dearer** |
| ECDSA P-256 | 85 201 ns | 113 018 ns | **1.33× — verify is dearer** |

Ed25519 verification is not cheaper than Ed25519 signing; it is the **most
lopsided of the two schemes**, by nearly a factor of two. That direction is
structural rather than an implementation accident: Ed25519 signing is one
fixed-base scalar multiplication over a table the implementation precomputes,
while verification is a double-base multiplication with the *public key* as one
base — no table, and no way to build one for a key seen once.

ECDSA is lopsided the same way and less so, and its signing side carries costs
Ed25519 does not (see below), which compresses its ratio from the other end.

The consequence for a design is the same in both columns and worth stating
plainly: **a token verified by N services costs N × the verification, and
verification is the expensive half.** If a fan-out is large, the symmetric
option — `mac.Verify` at 1.06 µs, 83× cheaper — is the trade to consider, and
`pkg/v1/token/BENCH.md` prices the same decision at the JWT layer.

## What the SDK costs over the bare primitive

`BenchmarkBareEd25519*` and `BenchmarkBareECDSAP256*` call the stdlib with the
keys **already parsed** — the shape a caller holding a live `*ecdsa.PrivateKey`
would have. The SDK's port is byte-slices in and out, so it re-derives the key
object on every call.

| | SDK | bare, key pre-parsed | envelope |
|---|---:|---:|---:|
| Ed25519 `Sign` | 36 498 ns · 1 alloc | 36 386 ns · 1 alloc | **0 %** |
| Ed25519 `Verify` | 88 044 ns · 0 allocs | 87 954 ns · 0 allocs | **0 %** |
| ECDSA `Sign` | 85 201 ns · 90 allocs | 53 287 ns · 58 allocs | **+60 %, +32 allocs** |
| ECDSA `Verify` | 113 018 ns · 20 allocs | 108 428 ns · 9 allocs | **+4 %, +11 allocs** |

**For Ed25519 the SDK is free — identical times, identical allocations.** Its
raw keys *are* the byte slices the port carries, so `Sign` is a length check and
a type conversion.

For ECDSA it is not, and the reason is DER. The SDK stores the private key as
SEC1 DER and the public key as PKIX DER, so `x509.ParseECPrivateKey` /
`x509.ParsePKIXPublicKey` run on every call. A CPU profile of
`sign.Sign(ECDSAP256, …)` splits the call without any subtraction:

```
crypto/ecdsa.SignASN1          69.27 %
crypto/x509.ParseECPrivateKey  29.89 %
crypto/sha256.Sum256            0.84 %
```

**Roughly 30 % of every ECDSA signature is re-parsing the key.** On the verify
side the arithmetic is exact rather than approximate: the envelope is 11
allocations, and `x509.ParsePKIXPublicKey` measured on its own is **11
allocations** — every allocation the facade adds is the DER decode, and
`ParsePKIXPublicKey` at 2 363 ns is the cheaper of the two parses by 9×.

This is the port's shape, not a defect in this package, and it is **not fixed
here**: a `sign` port that carried an opaque parsed-key type would move key
material into a Go object with a lifetime, which is the trade `pkg/v1/crypto`
makes explicitly for `Key` and the signature domain deliberately does not.
Recorded with its price tag — **25 µs and 23 allocations per ECDSA signature** —
so the trade can be argued with a number in ADR 0013.

One profile detail that will otherwise puzzle a reader: `sha512.blockAVX2` shows
up at 10.5 % of an *ECDSA-P256-SHA-256* signature. It is inside
`crypto/internal/fips140/ecdsa.Sign` — Go's FIPS-mode nonce derivation runs an
HMAC-DRBG over SHA-512. It is the stdlib's, not the SDK's, and not something a
caller can turn off.

## The two refusal paths, and why they differ by 15×

| 64 B | ns/op | vs valid verify |
|---|---:|---:|
| Ed25519, valid | 88 044 | 1.00× |
| Ed25519, message tampered | 90 218 | 1.02× |
| Ed25519, **signature malformed** | **5 710** | **0.065×** |
| ECDSA, valid | 113 018 | 1.00× |
| ECDSA, message tampered | 113 280 | 1.00× |
| ECDSA, signature malformed | 110 987 | 0.98× |

A **tampered message under a well-formed signature** — the realistic attack — is
refused for exactly what an acceptance costs, in both schemes. That is the
property that matters: a refusal costing *more* than an acceptance would be an
amplification an attacker gets for free.

A **malformed Ed25519 signature is refused 15× faster**, and that is correct and
harmless. Flipping the high byte of the 64-byte signature pushes its scalar `S`
outside the canonical range RFC 8032 §5.1.7 requires, and the bound is checked
*before* the scalar multiplication it would fund. Nothing leaks: a signature is
public, and the check is a property of the bytes, not of the key. The same flip
on a DER-encoded ECDSA signature usually leaves an in-range `s`, so the full
verification still runs — hence the difference.

One incidental finding, measured rather than assumed: the malformed and tampered
Ed25519 rows show **1 allocation of 16 bytes** where the valid row shows zero. An
allocation profile puts 99.9 % of it in `errors.New`, inside
`crypto/internal/fips140/ed25519.verifyWithDom` — the stdlib mints an error on
its failure path which `ed25519.Verify` then discards to return `false`. Not the
SDK's, not avoidable from here, and stated so nobody hunts for it.

## Key generation

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `GenerateKey(Ed25519)` | 30 090 | 96 | 2 |
| `GenerateKey(ECDSAP256)` | 33 643 | 3 684 | 79 |

Both are cheaper than one signature under their own scheme — ECDSA generates a
keypair for **40 %** of what it costs to sign with one, because signing pays the
DER parse and generation pays only the DER *marshal*. Neither is on a request
path, so this is a completeness row; it is here mainly to explain why every
benchmark above hoists key generation out of the timed loop.

## Reproducibility envelope

> **Numbers vary across machines.** This box is shared with fifteen other agent
> jobs and its load swings during a run. Each benchmark ran **three times**;
> every figure quoted above is the **fastest of the three** — the
> least-contaminated sample — and all three are printed below. The allocation
> columns are exact and do not vary. The RATIOS, especially verify/sign, are
> what this page claims.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, `sha_ni`, `sse4_2`, `avx2` |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 (`GOAMD64=v1`) |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | c30e2ad |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/sign
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkSignEd25519_64B-8               	   32637	     36521 ns/op	      64 B/op	       1 allocs/op
BenchmarkSignEd25519_64B-8               	   32889	     36498 ns/op	      64 B/op	       1 allocs/op
BenchmarkSignEd25519_64B-8               	   31647	     37390 ns/op	      64 B/op	       1 allocs/op
BenchmarkSignEd25519_4KiB-8              	   20906	     58782 ns/op	      64 B/op	       1 allocs/op
BenchmarkSignEd25519_4KiB-8              	   20340	     58808 ns/op	      64 B/op	       1 allocs/op
BenchmarkSignEd25519_4KiB-8              	   20748	     58082 ns/op	      64 B/op	       1 allocs/op
BenchmarkSignECDSAP256_64B-8             	   13639	     87178 ns/op	    7525 B/op	      90 allocs/op
BenchmarkSignECDSAP256_64B-8             	   13788	     85201 ns/op	    7524 B/op	      90 allocs/op
BenchmarkSignECDSAP256_64B-8             	   13702	     87749 ns/op	    7525 B/op	      90 allocs/op
BenchmarkSignECDSAP256_4KiB-8            	   13264	     90276 ns/op	    7526 B/op	      90 allocs/op
BenchmarkSignECDSAP256_4KiB-8            	   12178	     97716 ns/op	    7526 B/op	      90 allocs/op
BenchmarkSignECDSAP256_4KiB-8            	   12835	     92363 ns/op	    7526 B/op	      90 allocs/op
BenchmarkVerifyEd25519_64B-8             	   13590	     88129 ns/op	       0 B/op	       0 allocs/op
BenchmarkVerifyEd25519_64B-8             	   13622	     88044 ns/op	       0 B/op	       0 allocs/op
BenchmarkVerifyEd25519_64B-8             	   13533	     88642 ns/op	       0 B/op	       0 allocs/op
BenchmarkVerifyEd25519_4KiB-8            	   12254	     98337 ns/op	       0 B/op	       0 allocs/op
BenchmarkVerifyEd25519_4KiB-8            	   10000	    101727 ns/op	       0 B/op	       0 allocs/op
BenchmarkVerifyEd25519_4KiB-8            	   10000	    100762 ns/op	       0 B/op	       0 allocs/op
BenchmarkVerifyECDSAP256_64B-8           	   10000	    113018 ns/op	    1240 B/op	      20 allocs/op
BenchmarkVerifyECDSAP256_64B-8           	    9640	    116225 ns/op	    1240 B/op	      20 allocs/op
BenchmarkVerifyECDSAP256_64B-8           	   10000	    116090 ns/op	    1240 B/op	      20 allocs/op
BenchmarkVerifyECDSAP256_4KiB-8          	   10000	    127042 ns/op	    1240 B/op	      20 allocs/op
BenchmarkVerifyECDSAP256_4KiB-8          	   10000	    117438 ns/op	    1240 B/op	      20 allocs/op
BenchmarkVerifyECDSAP256_4KiB-8          	    9993	    125640 ns/op	    1240 B/op	      20 allocs/op
BenchmarkVerifyEd25519Tampered64B-8      	   13016	     91315 ns/op	      16 B/op	       1 allocs/op
BenchmarkVerifyEd25519Tampered64B-8      	   13446	     90218 ns/op	      16 B/op	       1 allocs/op
BenchmarkVerifyEd25519Tampered64B-8      	   13000	     91668 ns/op	      16 B/op	       1 allocs/op
BenchmarkVerifyECDSAP256Tampered64B-8    	   10000	    113280 ns/op	    1256 B/op	      21 allocs/op
BenchmarkVerifyECDSAP256Tampered64B-8    	   10000	    113364 ns/op	    1256 B/op	      21 allocs/op
BenchmarkVerifyECDSAP256Tampered64B-8    	   10000	    113358 ns/op	    1256 B/op	      21 allocs/op
BenchmarkVerifyEd25519Malformed64B-8     	  210518	      5710 ns/op	      16 B/op	       1 allocs/op
BenchmarkVerifyEd25519Malformed64B-8     	  210133	      5790 ns/op	      16 B/op	       1 allocs/op
BenchmarkVerifyEd25519Malformed64B-8     	  210574	      5725 ns/op	      16 B/op	       1 allocs/op
BenchmarkVerifyECDSAP256Malformed64B-8   	   10000	    113262 ns/op	    1256 B/op	      21 allocs/op
BenchmarkVerifyECDSAP256Malformed64B-8   	   10000	    110987 ns/op	    1256 B/op	      21 allocs/op
BenchmarkVerifyECDSAP256Malformed64B-8   	   10000	    112427 ns/op	    1256 B/op	      21 allocs/op
BenchmarkGenerateKeyEd25519-8            	   39418	     30413 ns/op	      96 B/op	       2 allocs/op
BenchmarkGenerateKeyEd25519-8            	   39832	     30090 ns/op	      96 B/op	       2 allocs/op
BenchmarkGenerateKeyEd25519-8            	   39846	     30589 ns/op	      96 B/op	       2 allocs/op
BenchmarkGenerateKeyECDSAP256-8          	   33282	     33998 ns/op	    3683 B/op	      79 allocs/op
BenchmarkGenerateKeyECDSAP256-8          	   36402	     33643 ns/op	    3685 B/op	      79 allocs/op
BenchmarkGenerateKeyECDSAP256-8          	   35241	     34718 ns/op	    3684 B/op	      79 allocs/op
BenchmarkBareEd25519Sign-8               	   32912	     36386 ns/op	      64 B/op	       1 allocs/op
BenchmarkBareEd25519Sign-8               	   31545	     37215 ns/op	      64 B/op	       1 allocs/op
BenchmarkBareEd25519Sign-8               	   32815	     37143 ns/op	      64 B/op	       1 allocs/op
BenchmarkBareEd25519Verify-8             	   13700	     87954 ns/op	       0 B/op	       0 allocs/op
BenchmarkBareEd25519Verify-8             	   13158	     90546 ns/op	       0 B/op	       0 allocs/op
BenchmarkBareEd25519Verify-8             	   13449	     89047 ns/op	       0 B/op	       0 allocs/op
BenchmarkBareECDSAP256Sign-8             	   21272	     56139 ns/op	    6032 B/op	      58 allocs/op
BenchmarkBareECDSAP256Sign-8             	   21870	     53287 ns/op	    6032 B/op	      58 allocs/op
BenchmarkBareECDSAP256Sign-8             	   21468	     54723 ns/op	    6032 B/op	      58 allocs/op
BenchmarkBareECDSAP256Verify-8           	    9724	    110471 ns/op	     544 B/op	       9 allocs/op
BenchmarkBareECDSAP256Verify-8           	   10000	    109131 ns/op	     544 B/op	       9 allocs/op
BenchmarkBareECDSAP256Verify-8           	   10000	    108428 ns/op	     544 B/op	       9 allocs/op
BenchmarkParseECPrivateKey-8             	   44403	     24257 ns/op	    1080 B/op	      23 allocs/op
BenchmarkParseECPrivateKey-8             	   49804	     22743 ns/op	    1080 B/op	      23 allocs/op
BenchmarkParseECPrivateKey-8             	   54022	     22148 ns/op	    1080 B/op	      23 allocs/op
BenchmarkParsePKIXPublicKey-8            	  497506	      2363 ns/op	     696 B/op	      11 allocs/op
BenchmarkParsePKIXPublicKey-8            	  496231	      2447 ns/op	     696 B/op	      11 allocs/op
BenchmarkParsePKIXPublicKey-8            	  497613	      2630 ns/op	     696 B/op	      11 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/sign	70.764s
```
