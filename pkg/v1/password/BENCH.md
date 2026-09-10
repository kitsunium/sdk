<!-- generated from pkg/v1/password/password_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./v1/password/` to refresh -->
# Benchmarks — `pkg/v1/password`

PBKDF2-SHA256 at 600 000 iterations. **Every number on this page is five orders
of magnitude larger than anything else in the crypto family, and that is the
feature.** A password hash is slow so that an attacker's guess costs what an
honest login costs; a benchmark that reported it as "slow" without saying so
would be describing a security property as a defect.

What a benchmark *can* usefully add is the number nobody publishes: **the cost
per iteration**, which is the knob an operator actually turns. It is at the
bottom of this page. The family-wide choice table lives in
[`pkg/v1/crypto/BENCH.md`](../crypto/BENCH.md).

## The choice table

| | ns/op | human | B/op | allocs |
|---|---:|---:|---:|---:|
| `Verify`, malformed PHC | **120.8** | 121 ns | 48 | 1 |
| `NeedsRehash` | **520.4** | 520 ns | 208 | 4 |
| `Verify`, correct password | 144 251 500 | **144 ms** | 1 012 | 15 |
| `Verify`, wrong password | 144 460 550 | **144 ms** | 1 012 | 15 |
| `Hash` | 144 801 255 | **145 ms** | 1 248 | 19 |

The four rows that matter, in one line each:

- **Budget 145 ms of CPU per login.** One core sustains **6.9 logins/second**;
  eight cores sustain about 55. That is the capacity planning number, and it is
  the one most often discovered in production instead of in a table.
- **A wrong password costs the same as a right one** — 0.14 % apart. That is
  what makes the endpoint not an oracle, and it is measured below rather than
  asserted.
- **A malformed stored hash is refused in 121 ns**, 1.2 million times cheaper.
  The PHC string is parsed before any stretching, so a lookup that returns
  garbage is not a free denial-of-service lever.
- **`NeedsRehash` is 520 ns**, so calling it after every successful login —
  which is the documented upgrade-on-verify pattern — costs nothing measurable
  against the 144 ms verify that preceded it.

## The tuning knob: 237 ns per iteration

`Hash`'s cost is the iteration count and nothing else, and this is the pair that
proves it rather than claiming it:

| bare `pbkdf2.Key(sha256.New, …)` | ns/op |
|---|---:|
| 1 iteration | **1 567** |
| 600 000 iterations (the shipped policy) | **141 921 184** |

`(141 921 184 − 1 567) / 600 000` = **236.5 ns per iteration**, with a fixed
setup of about 1.6 µs. So on this machine:

| iterations | cost | note |
|---:|---:|---|
| 100 000 | 23.7 ms | below current OWASP guidance |
| 300 000 | 71.0 ms | |
| **600 000** | **141.9 ms** | **what `service/crypto/pbkdf2pw` ships** |
| 1 000 000 | 236.5 ms | |

An operator who needs a different budget multiplies. That is the whole point of
publishing the per-iteration figure instead of only the total: **the total is
not portable and the ratio is.**

One measured detail, recorded and deliberately not pursued: 236.5 ns per
iteration is about **5.2 SHA-256 compressions** on this CPU (a compression is
45.2 ns here, from `pkg/v1/mac/BENCH.md`'s 1 MiB row), where the construction
needs only two. The difference is `crypto/hmac`'s per-iteration `Reset`
restoring its marshalled inner and outer states. **It is not worth chasing, and
not only because it is stdlib**: making a password KDF cheaper per iteration
makes it weaker at a fixed iteration count. The correct response to "PBKDF2
could be 2.5× faster" is to raise the iteration count by 2.5×, which lands back
here. Optimising this path is a security regression wearing a performance
report's clothes.

## What the SDK costs over the bare primitive: 2 %

| | ns/op | allocs |
|---|---:|---:|
| bare `pbkdf2.Key`, 600 000 iterations | 141 921 184 | 11 |
| `password.Hash` | 144 801 255 | 19 |

**+2.9 ms on 142 ms — 2.03 %** — for a fresh 32-byte random salt, the base64
encoding of salt and digest, and the PHC string assembly. Eight extra
allocations totalling 444 bytes.

That is the entire facade. Everything else on this page is `crypto/pbkdf2` doing
what it was configured to do.

## The oracle check — corroboration, not proof

| | ns/op | allocs |
|---|---:|---:|
| `Verify`, correct password | 144 251 500 | 15 |
| `Verify`, wrong password | **144 460 550** | 15 |

**0.14 % apart, with the wrong password marginally dearer**, and identical in
allocations.

What that does and does not establish is worth being precise about. A benchmark
at this resolution cannot *prove* constant time — the run-to-run spread on this
box (3.5 %) is 25× the gap being measured, so a difference of a few hundred
nanoseconds would hide inside it. The guarantee is **structural**: the stored
hash is recomputed in full from the PHC's own salt and iteration count, and
`subtle.ConstantTimeCompare` decides the result, so there is no path that
returns early on a mismatch and no branch on secret data.

What the benchmark rules out is the failure mode a *structural* reading can
miss: a short-circuit large enough to matter operationally. An implementation
that bailed after the first mismatching digest byte would refuse a wrong
password in **microseconds** against 144 milliseconds, and that would be
unmissable here. It is not there.

The refusal that *is* fast is the structural one, and it is meant to be: an
unparseable PHC string is rejected in **121 ns**, before the 600 000 iterations
it would otherwise fund. That ordering — bound first, work second — is the same
class of check ADR 0042 names for tokens, and it is load-bearing here, because
the string comes from a database row an attacker may influence.

## Reproducibility envelope

> **Numbers vary across machines**, and on this page that is the *only*
> caveat that matters: the iteration count is a policy constant, the per-
> iteration cost is hardware, and 600 000 iterations that take 142 ms here will
> take a different time on your production CPU. **Re-measure before choosing an
> iteration count.** The 237 ns/iteration figure is the portable form of this
> page.
>
> This box is shared with fifteen other agent jobs. Each benchmark ran **three
> times**; every figure quoted above is the **fastest of the three**. At
> 145 ms/op a one-second `-benchtime` yields only 7–8 iterations per run, so
> these rows are the noisiest in the family: **eight of the nine
> `Hash`/`Verify`/`VerifyWrong` samples fall within 3.5 %**, and the ninth — a
> `Hash` sample at 159 ms, 10 % high — is visible in the block below and is why
> this page quotes minima rather than means. Even at that spread the conclusions
> drawn here (a 0.14 % right-vs-wrong gap is *within* it; a 1.2-million-fold
> malformed-PHC gap is far outside it) are unaffected.

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
pkg: github.com/kitsunium/sdk/pkg/v1/password
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkHash-8                      	       7	 147162473 ns/op	    1485 B/op	      22 allocs/op
BenchmarkHash-8                      	       7	 159239846 ns/op	    1245 B/op	      19 allocs/op
BenchmarkHash-8                      	       7	 144801255 ns/op	    1248 B/op	      19 allocs/op
BenchmarkVerify-8                    	       7	 144586341 ns/op	    1012 B/op	      15 allocs/op
BenchmarkVerify-8                    	       7	 145860320 ns/op	    1012 B/op	      15 allocs/op
BenchmarkVerify-8                    	       7	 144251500 ns/op	    1012 B/op	      15 allocs/op
BenchmarkVerifyWrong-8               	       7	 146408555 ns/op	    1012 B/op	      15 allocs/op
BenchmarkVerifyWrong-8               	       7	 149244025 ns/op	    1012 B/op	      15 allocs/op
BenchmarkVerifyWrong-8               	       8	 144460550 ns/op	    1012 B/op	      15 allocs/op
BenchmarkVerifyMalformed-8           	 9642194	       125.2 ns/op	      48 B/op	       1 allocs/op
BenchmarkVerifyMalformed-8           	 9482116	       120.8 ns/op	      48 B/op	       1 allocs/op
BenchmarkVerifyMalformed-8           	10675164	       125.0 ns/op	      48 B/op	       1 allocs/op
BenchmarkNeedsRehash-8               	 2278479	       520.4 ns/op	     208 B/op	       4 allocs/op
BenchmarkNeedsRehash-8               	 2075913	       589.4 ns/op	     208 B/op	       4 allocs/op
BenchmarkNeedsRehash-8               	 2206558	       567.4 ns/op	     208 B/op	       4 allocs/op
BenchmarkBarePBKDF2_1Iter-8          	  696230	      1617 ns/op	     804 B/op	      11 allocs/op
BenchmarkBarePBKDF2_1Iter-8          	  751015	      1567 ns/op	     804 B/op	      11 allocs/op
BenchmarkBarePBKDF2_1Iter-8          	  924207	      1608 ns/op	     804 B/op	      11 allocs/op
BenchmarkBarePBKDF2_CurrentIters-8   	       8	 143490578 ns/op	     804 B/op	      11 allocs/op
BenchmarkBarePBKDF2_CurrentIters-8   	       8	 141921184 ns/op	     804 B/op	      11 allocs/op
BenchmarkBarePBKDF2_CurrentIters-8   	       7	 144318069 ns/op	     806 B/op	      11 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/password	25.220s
```
