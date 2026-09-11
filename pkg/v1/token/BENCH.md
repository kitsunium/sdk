<!-- generated from pkg/v1/token/token_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/token/` to refresh -->
# Benchmarks — `pkg/v1/token`

JWT over JWS Compact and PASETO v4.public (ADR 0042). **Verification runs once
per authenticated request**; issuing runs once per login. The two have very
different budgets, and the algorithms differ by more than an order of magnitude,
which is the fact a caller needs before choosing one.

## The choice table

| | issue | verify | verify allocs |
|---|---:|---:|---:|
| **HS256** (JWS) | **8 482 ns** | **20 528 ns** | 87 |
| EdDSA (JWS) | 47 351 ns | 111 830 ns | 80 |
| PASETO v4.public | 46 682 ns | 104 811 ns | 56 |
| ES256 (JWS) | 64 424 ns | 134 205 ns | 100 |

**HS256 verifies 5–6× faster than any asymmetric option**, which is what a
symmetric MAC buys — and what it costs is that every verifier holds the key that
can also MINT tokens. That trade is the decision, and this table is its price
tag; ADR 0042 is where the security half is argued.

Among the asymmetric three, **PASETO v4.public is the cheapest on both sides**
even though it uses the same Ed25519 primitive as JWS EdDSA. The cryptography is
held constant across those two rows on purpose, so the difference — 7 019 ns on
verify, and **24 fewer allocations** — is the ENVELOPE: PASETO has no header to
parse, no `alg` to compare, and no base64url header segment.

ES256 is the most expensive on both sides. ECDSA P-256 verification is
inherently costly in Go, and it is not this SDK's to fix.

## The refusal paths, which is where a verifier lives

A verifier is exposed to input the caller does not choose, so a refusal that
costs more than an acceptance is an amplification an attacker gets for free.

| | ns/op | allocs |
|---|---:|---:|
| `Verify_HS256` (valid) | 20 528 | 87 |
| `Verify_HS256_Tampered` | **6 419** | 34 |
| `Verify_HS256_Oversize` (64 KiB, cap 4 KiB) | **325.2** | 2 |

A tampered signature is refused for **less than a third** of what accepting a
good token costs. And a 64 KiB token against a 4 KiB `MaxTokenLen` is refused in
**325 ns** — the bound is checked BEFORE the work it funds, which is the
CVE-2025-30204 class ADR 0042 names, and this is that check being load-bearing
rather than merely present.

## One optimisation the profile found, and its honest size

`Verify_HS256` was **89 allocations**. A memory profile put 56.5 % of allocated
objects in `decodeClaims`, and inside it, **23 %** in `skipValue` — which used
`json.RawMessage` to consume a value in the DUPLICATE-MEMBER pass. That pass
discards every value it reads, so every byte copied was waste.

Replacing it with a depth-tracking `Token()` walk:

| | before | after |
|---|---:|---:|
| `Verify_HS256` | 23 544 ns · 4 044 B · 89 allocs | **20 528 ns · 3 900 B · 87 allocs** |
| `Verify_EdDSA` | 115 760 ns · 3 533 B · 82 allocs | **111 830 ns · 3 389 B · 80 allocs** |

**Two allocations and about 14 %** — real, reproducible over `-count=2`, and
deliberately not oversold. The gain scales with how many members are skipped;
this benchmark's claim set has six.

## What the remaining 87 allocations are, and why they stay

They are the JSON claim decoding, and they are the design rather than an
oversight. A verification walks the claim payload **three times**: once to bound
its nesting depth, once to refuse a duplicate member (RFC 8725 §2.6 — a token
with two `aud` members says one thing to a Go reader and another to a reader
that keeps the first), and once to decode. Each pass exists because a shortcut
in it is a substitution vector.

Collapsing them into one pass is possible and is **not** attempted here: it
means hand-writing a JSON walker for the most security-sensitive decoder in the
SDK, to save microseconds on a path already dominated — for every asymmetric
algorithm — by the signature check. Recorded as the next step if a profile of a
real service ever puts token verification on top, which at 20 µs for HS256 it
will not.

## Issuing

`NewClaims` plus six `With*` calls is **390.2 ns / 1 alloc**, so claim assembly
is not where issuing goes. `Issue_HS256` at 8 482 ns / 42 allocs is JSON
encoding, two base64url segments and one HMAC; the asymmetric issuers add their
signature and nothing else structural.

## Reproducibility envelope

> **Numbers vary across machines.** This run shared the box with four other
> jobs. The allocation column is exact and does not vary; the nanoseconds do,
> and the RATIOS between algorithms are what this table asserts.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 6704f4b |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/token
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkIssue_HS256-8             	  142128	      8482 ns/op	    3186 B/op	      42 allocs/op
BenchmarkVerify_HS256-8            	   57955	     20528 ns/op	    3900 B/op	      87 allocs/op
BenchmarkIssue_ES256-8             	   18612	     64424 ns/op	    9088 B/op	      98 allocs/op
BenchmarkVerify_ES256-8            	    8920	    134205 ns/op	    4576 B/op	     100 allocs/op
BenchmarkIssue_EdDSA-8             	   24561	     47351 ns/op	    2834 B/op	      36 allocs/op
BenchmarkVerify_EdDSA-8            	   10000	    111830 ns/op	    3389 B/op	      80 allocs/op
BenchmarkIssue_PasetoV4-8          	   25605	     46682 ns/op	    3106 B/op	      36 allocs/op
BenchmarkVerify_PasetoV4-8         	   10000	    104811 ns/op	    2380 B/op	      56 allocs/op
BenchmarkVerify_HS256_Tampered-8   	  166162	      6419 ns/op	    1986 B/op	      34 allocs/op
BenchmarkVerify_HS256_Oversize-8   	 3808708	       325.2 ns/op	     208 B/op	       2 allocs/op
BenchmarkNewClaims-8               	 3084100	       390.2 ns/op	      16 B/op	       1 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/token	12.848s
```
