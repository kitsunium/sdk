<!-- generated from third-party/x-crypto/argon2id/argon2id_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./third-party/x-crypto/argon2id/` from the repo root; every figure below is the median of NINE samples over THREE such processes -->
# Benchmarks — `third-party/x-crypto/argon2id`

**Every number on this page is large on purpose, and none of it is overhead.**
A password hash is slow and memory-hungry so that an attacker's guess costs what
an honest login costs; a report that filed those milliseconds as a defect would
be describing a security property as a bug.

So this page is not a speed report. It is a **calibration table**: what each of
argon2's three knobs — memory, iterations, parallelism — buys and costs on real
hardware, so an operator can choose a latency budget for their own box instead
of inheriting one. The shipped policy is the OWASP 2023 set, `m=19456` KiB
(19 MiB), `t=2`, `p=1`.

The reference arm is `internal/service/crypto/pbkdf2pw` — the password hash a
consumer gets **without** this dependency — measured in the same process, in the
same run, under the same load.

## The shipped policy, and what a login costs

| | ns/op | human | B/op | allocs |
|---|---:|---:|---:|---:|
| `Verify`, malformed PHC | **114.3** | 114 ns | 64 | 1 |
| `NeedsRehash` | **2 720** | 2.7 µs | 272 | 11 |
| `Verify`, wrong password | 33 616 555 | **33.6 ms** | 19 927 871 | 40 |
| `Hash` | 34 344 488 | **34.3 ms** | **19 926 768** | 34 |
| `Verify`, correct password | 34 302 634 | **34.3 ms** | 19 927 899 | 40 |
| — | | | | |
| PBKDF2-SHA256 `Hash` (600 000 iters) | 142 033 404 | **142.0 ms** | 1 279 | 20 |
| PBKDF2-SHA256 `Verify` | 140 886 912 | **140.9 ms** | 938 | 14 |

Four readings, one line each:

- **Budget 34 ms and 19.9 MB of live heap per login.** One core sustains
  **29 logins/second**; the memory is the number people forget, and it is below.
- **argon2id is 4.1× FASTER than the SDK's default PBKDF2** at each scheme's
  shipped policy — 34 ms against 142 ms. You do not buy argon2id with latency.
  You buy it with **15 600× the memory**, and that memory is the entire defence.
- **A malformed stored hash is refused in 114 ns**, 300 000× cheaper than a real
  verify. The field count is checked before anything else, so a corrupt database
  row is not a free denial-of-service lever. (It is not the *whole* story — see
  "What the caps do not bound" below.)
- **A wrong password costs the same as a right one** (2.0 %, inside this page's
  noise floor and observed with either sign across runs), which is what keeps the
  endpoint from being a timing oracle.

## `-benchmem` really does report the memory parameter

The task this page exists for turns on a knob `-benchmem` is usually blind to,
so it was checked rather than assumed. `golang.org/x/crypto/argon2` allocates the
**entire block matrix in a single `make`** — `B := make([]block, memory)` in
`initBlocks`, where `block` is `[128]uint64` — so `B/op` captures it exactly.
The allocation profile of one `Hash` says so in one line:

```
      flat  flat%   sum%        cum   cum%
    1.84GB 99.69% 99.69%     1.84GB 99.69%  golang.org/x/crypto/argon2.initBlocks
         0     0% 99.69%     1.84GB 99.71%  …/third-party/x-crypto/argon2id.argon2idPW.Hash
         0     0% 99.69%     1.84GB 99.71%  golang.org/x/crypto/argon2.IDKey (inline)
         0     0% 99.69%     1.84GB 99.71%  golang.org/x/crypto/argon2.deriveKey
```

And the arithmetic closes at every rung of the ladder:

| configured `m` | `m` × 1024 B | measured B/op | difference |
|---:|---:|---:|---:|
| 8 MiB | 8 388 608 | 8 390 829 | +2 221 |
| 19 MiB (shipped) | 19 922 944 | 19 925 156 | +2 212 |
| 128 MiB | 134 217 728 | 134 220 020 | +2 292 |

**`B/op` = `m` × 1024 + about 2.2 KB**, and the 2.2 KB is the same constant at
every size — it is the salt, the base64 and the PHC string, not the KDF. So the
memory parameter is directly readable from any `-benchmem` run, and there is no
need for an RSS measurement to see it.

The operational form of that is the sentence most easily missed:

> **Peak RSS is roughly `concurrency × 19.9 MB`.** Eight concurrent logins is
> 159 MB; a hundred is **2.0 GB**. PBKDF2 at 1.3 KB per call has no such
> requirement, so migrating to argon2id adds a memory dimension to capacity
> planning that did not exist before. Size the login path's concurrency limit,
> not just its CPU.

## Calibration — the memory knob

`t=2`, `p=1` held; bare `argon2.IDKey`, so the numbers are the KDF's own.

| `m` | ns/op | human | ms per MiB | B/op |
|---:|---:|---:|---:|---:|
| 8 MiB | 13 535 211 | 13.5 ms | 1.692 | 8 390 829 |
| 16 MiB | 27 874 087 | 27.9 ms | 1.742 | 16 779 541 |
| **19 MiB — shipped** | **33 606 756** | **33.6 ms** | **1.769** | 19 925 156 |
| 32 MiB | 56 799 434 | 56.8 ms | 1.775 | 33 556 759 |
| 64 MiB | 123 829 544 | 123.8 ms | 1.935 | 67 111 086 |
| 128 MiB | 261 991 102 | 262.0 ms | **2.047** | 134 220 020 |

**Memory cost is linear to about 32 MiB and super-linear beyond it** — the price
per MiB climbs 21 % from the 8 MiB rung to the 128 MiB one. That is not a defect;
it is the mechanism. Once the matrix stops fitting in cache the algorithm starts
paying DRAM latency on every dependent read, which is precisely the cost an
attacker's GPU or ASIC cannot buy its way out of. **Raising `m` buys more than
proportionally more defence**, which is why RFC 9106 makes memory the primary
parameter.

## Calibration — the iteration knob

`m=19456` KiB, `p=1` held.

| `t` | ns/op | human | delta from previous |
|---:|---:|---:|---:|
| 1 | 17 681 505 | 17.7 ms | — |
| **2 — shipped** | **33 492 731** | **33.5 ms** | +15.8 ms |
| 3 | 49 663 177 | 49.7 ms | +16.2 ms |
| 4 | 65 173 505 | 65.2 ms | +15.5 ms |
| 6 | 96 408 434 | 96.4 ms | +15.6 ms (×2) |

**Exactly linear, at 15.7 ms per pass with a 1.9 ms fixed cost**:

> `cost(t) ≈ 1.9 ms + t × 15.7 ms`, at `m = 19 MiB`, on this CPU.

Checked against rungs the fit was not drawn through: `t=3` predicts 49.2 ms and
measured 49.7 (1.0 % out); `t=4` predicts 64.9 and measured 65.2 (0.4 % out).
The 1.9 ms intercept is the matrix allocation, its zeroing and the initial fill —
work that happens once no matter how many passes follow.

Divide through and the portable figure falls out: **0.884 ms per MiB per pass.**
That is the number to carry to another machine; the totals on this page are not
portable and the slope is.

## Calibration — the parallelism knob, which is not a cost knob

`m=19456` KiB, `t=2` held.

| `p` | ns/op | human | speedup | B/op | core-ms consumed |
|---:|---:|---:|---:|---:|---:|
| **1 — shipped** | **33 335 219** | **33.3 ms** | 1.00× | 19 925 111 | 33.3 |
| 2 | 21 350 386 | 21.4 ms | 1.56× | 19 926 276 | 42.7 |
| 4 | 15 393 600 | 15.4 ms | 2.17× | 19 928 613 | 61.6 |
| 8 | 13 008 029 | 13.0 ms | **2.56×** | 19 933 285 | 104.1 |

Two facts, and the second is a security statement rather than a performance one.

**Parallelism costs no memory.** The four `B/op` values differ by 0.04 % across
an eight-fold change in `p` — argon2 splits the *same* matrix into lanes rather
than allocating one per lane. So `p` is free of the RSS consequence that `m` has.

**And `p` does not make the hash harder — it makes it finish sooner.** Total work
is `t × m` block operations regardless of `p`; the last column shows the CPU
*consumed* rising from 33.3 to 104.1 core-milliseconds as synchronisation
overhead accumulates, while the wall clock falls. An attacker does not care about
your wall clock: they run guesses in parallel at `p=1` and get the same
throughput per core either way.

> **Raising `p` to hit a latency target and stopping there weakens the policy.**
> Going from `p=1` to `p=8` cuts a login from 33.3 ms to 13.0 ms and leaves the
> attacker's cost per guess unchanged. If `p` is raised, `m` or `t` must be raised
> to put the cost back. The speedup is also only 2.56× on eight cores — 32 %
> efficiency — because the lanes synchronise four times per pass, so `p` is a
> poor way to buy latency in the first place.

## The three ladders agree with each other

The shipped configuration appears once in each ladder, measured three times
independently as three different rows of three different benchmarks:

| the same `m=19456, t=2, p=1` computation | ns/op |
|---|---:|
| bottom of the memory ladder | 33 606 756 |
| middle of the iteration ladder | 33 492 731 |
| top of the parallelism ladder | 33 335 219 |

**Within 0.8 % of each other.** That is the internal consistency check that makes
the rest of the calibration trustworthy: three code paths, three positions in the
run, one answer.

## Where the time actually goes

The CPU profile of one `Hash` at the shipped policy:

```
      flat  flat%   sum%        cum   cum%
     1.67s 46.65% 46.65%      1.67s 46.65%  golang.org/x/crypto/argon2.blamkaSSE4
     0.81s 22.63% 69.27%      0.81s 22.63%  golang.org/x/crypto/argon2.mixBlocksSSE2
     0.41s 11.45% 80.73%      0.41s 11.45%  runtime.memclrNoHeapPointers
     0.34s  9.50% 90.22%      0.34s  9.50%  golang.org/x/crypto/argon2.xorBlocksSSE2
     0.10s  2.79% 93.02%      2.92s 81.56%  golang.org/x/crypto/argon2.processBlockSSE
     0.06s  1.68% 94.69%      0.10s  2.79%  golang.org/x/crypto/argon2.indexAlpha
     0.04s  1.12% 95.81%      0.04s  1.12%  golang.org/x/crypto/argon2.phi (inline)
     0.03s  0.84% 96.65%      3.06s 85.47%  golang.org/x/crypto/argon2.processBlocks.func1
```

**78.8 % is the compression function in SSE assembly** and 11.45 % is the Go
runtime zeroing the 19 MiB matrix Go semantics require it to zero before argon2
overwrites it — a real, unavoidable ninth of the cost of every hash.

One detail worth recording because it changes what these numbers mean elsewhere:
the routine is `blamkaSSE4`, **not an AVX2 one, and this CPU advertises `avx2`**.
`golang.org/x/crypto/argon2` ships `blamka_amd64.go` with SSE2/SSE4 assembly and
selects it on `cpu.X86.HasSSE41`; there is no AVX2 path in the library at all. So
every figure on this page is SSE-era throughput on an AVX2 machine. That is not
something to fix here — it is upstream, and it is cryptographic code — but an
operator comparing against a C implementation's published numbers should know why
theirs are faster.

(That same file is also why this package is quarantined: `blamka_amd64.go`
imports `golang.org/x/sys/cpu`, the dependency the four inner modules ban. The
root `go.mod` carries it as an indirect. ADR 0012's placement is load-bearing,
not stylistic.)

## The constant-time comparison — corroborated, and left alone

`Verify` recomputes the digest in full from the stored PHC's own salt, costs and
output length, and decides with `subtle.ConstantTimeCompare`. There is no path
that returns early on a mismatch and no branch on secret data.

| | ns/op | allocs |
|---|---:|---:|
| `Verify`, correct password | 34 302 634 | 40 |
| `Verify`, wrong password | **33 616 555** | 40 |

**2.0 % apart, and identical in allocations.** Be precise about what that does and
does not establish: the spread on these rows is 3–6 %, larger than the gap being
measured, and **the sign of the gap flips between runs** — an earlier three-process
set had the wrong password 2 % *dearer*, this one has it 2 % cheaper. A benchmark
at this resolution **cannot prove constant time**, and the alternating sign is the
cleanest evidence that what is being measured is noise rather than a difference.

The guarantee is structural. What the benchmark *does* rule out is the failure a
structural reading can miss — a short-circuit big enough to matter operationally.
An implementation that bailed on the first mismatching digest byte would refuse a
wrong password in microseconds against 34 milliseconds, and that would be
unmissable here. It is not there.

**This comparison is not to be optimised, shortened, or replaced with
`bytes.Equal`.** It is the reason the row above exists.

## What the caps do not bound — reported, not changed

`validCosts` refuses a stored PHC whose costs exceed `maxMem` = 2 GiB,
`maxTime` = 2²⁰, `maxThreads` = 255, and `decodeSaltDigest` pins the digest to
exactly 32 bytes so a hostile PHC cannot make `Verify` derive an
attacker-chosen-length key. The comment on `validCosts` says the caps "bound the
work + allocation a hostile or corrupt stored hash can request".

The **allocation** half is true and tight: 2 GiB, one `make`, refused above it.
The **work** half does not follow, because the caps are independent and the cost
is their product. Scaling the measured 128 MiB rung:

| a stored PHC declaring… | cost of ONE `Verify` (lower bound) | RSS |
|---|---:|---:|
| `m=2GiB, t=1` | **≥ 2.2 s** | 2 GiB |
| `m=2GiB, t=2` | ≥ 4.2 s | 2 GiB |
| `m=2GiB, t=2²⁰` | ≥ 25 days | 2 GiB |
| `m=19MiB, t=2²⁰` | ≥ 4.6 hours | 19.9 MB |

These are lower bounds obtained by linear scaling from the measured 262.0 ms at
`m=128 MiB, t=2`; the ladder is super-linear in `m`, so the true figures are
higher. **One row in the users table is enough to occupy a core for hours and 2
GiB of RAM**, inside the declared caps and against a `Verify` call the
application believes is bounded.

Whether that matters depends on a threat model this page cannot settle — if
stored hashes are never attacker-influenced, the caps are ample. **It is reported
and deliberately not changed**: lowering `maxMem` or `maxTime` is a security
parameter decision with a compatibility consequence (a legitimately stronger
stored hash would stop verifying), and this file's mandate is to measure. The
natural fix, if one is wanted, is a *product* bound rather than two independent
ones.

## Refused

- **Replacing `fmt.Sscanf` + `fmt.Sprintf` in `parseParams` with `strconv`.**
  It is the obvious optimisation — `NeedsRehash` costs 2 720 ns and 11
  allocations, 5× what the sibling PBKDF2 scheme's costs, and virtually all of it
  is `fmt`. Refused on both counts. The saving is ~2.5 µs on a path that is
  **0.008 % of a `Verify`**, and the `Sprintf` is not formatting — it is the
  canonicality check, the thing that rejects `m=019456` or a trailing-junk cost
  field that `Sscanf` would silently accept. Removing it to save microseconds
  would trade a parser hardening for a rounding error.
- **Lowering any shipped cost parameter.** `m=19456, t=2, p=1` is OWASP 2023.
  Nothing on this page is an argument to reduce it, and a "faster password hash"
  is a weaker one at a fixed parameter set.
- **Reusing the block matrix across calls** to avoid the 11.45 % spent in
  `memclrNoHeapPointers`. Refused: it would hold 19 MiB of password-derived
  state alive between logins, and the zeroing is what makes the reuse safe in the
  first place.

Nothing in this package was changed to produce this report.

## Rows thrown away, and what was wrong with them

**A benchmark reporting that the facade has negative cost.** An early full run put
`Hash` at 31.8 ms and the bare `argon2.IDKey` it wraps at 34.1 ms — the wrapper
6.8 % *faster* than the thing it wraps, which is impossible. Worse, it survived a
second and a third process: across nine samples `Hash` stayed at 31.8 ms while
four bare rungs of the identical computation clustered at 33.75–34.13 ms.

Running each alone in its own process settled the direction (`Hash` 33.76 ms
against `BareIDKey` 33.57 ms — 0.6 % apart and correctly ordered), and the clean
three-process set published above settles it properly:

| nine samples, three processes | ns/op | spread |
|---|---:|---:|
| `BareIDKey` | 33 657 099 | 5.3 % |
| `Hash` | **34 344 488** | 5.8 % |

**+2.0 %, in the correct direction.** The cause of the original inversion was not
the code, it was the protocol: at ~34 ms/op a one-second `-benchtime` yields
**34 iterations**, and samples taken inside one process are correlated — they
share a heap state, a scavenger state and a slice of this shared VM's luck. A
single `-count=3` therefore reports a *precision* it does not have.

Which still leaves the facade's cost unmeasurable by subtraction — +2.0 % of
34 ms is 687 µs, and that is inside the 5–6 % spread of both rows. So it is
bounded from the other side instead. Everything this package does outside the KDF
is PHC handling, and PHC handling is measured directly: `NeedsRehash`, which is
`decodePHC` plus three integer comparisons and nothing else, costs **2 720 ns**.
`encodePHC` does strictly less work than `decodePHC` (two base64 encodes and one
`Sprintf`, against a split, an `Sscanf`, a `Sprintf` and two decodes). So:

> **The SDK facade costs under 3 µs — under 0.01 % of a 34 ms hash.** That is a
> bound derived from a measured microsecond-scale row, not a difference between
> two noisy 34-millisecond ones, and it is the only form of the claim this
> machine can support.

## Reproducibility envelope

> **Numbers vary across machines, and on this page that is the point, not a
> caveat.** The cost parameters are policy constants; the milliseconds they buy
> are hardware. **Re-measure before choosing a parameter set.** The portable
> forms of this page are `0.884 ms per MiB per pass` and the two slopes in the
> calibration sections.
>
> This VM is memory-ballooned (15.6 GiB nominal, 8 GiB balloon floor), which
> interacts with argon2's whole purpose. **The ladder was deliberately capped at
> 128 MiB** — 6.7× the shipped policy, enough to expose the super-linear region —
> rather than run to the 2 GiB `maxMem`, because a benchmark that provokes the
> balloon measures the hypervisor. Peak RSS of the test process was not
> instrumented and is deliberately not claimed here; what WAS observed is
> `MemAvailable` — 10.7 GB before the run and 10.8 GB after — so the balloon was
> never provoked.
>
> Every figure is the **median of nine samples over three separate processes**;
> see "Rows thrown away" for why three counts in one process was not enough. The
> per-row spread is 2–9 % except `m=128MiB` (19.6 %) and `p=8` (15.6 %), the two
> rows with the fewest iterations per sample.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, `sha_ni`, `sse4_1`, `sse4_2`, `avx2` |
| SIMD path taken    | **SSE4** (`blamkaSSE4`) — x/crypto ships no AVX2 argon2 |
| RAM                | 15.6 GiB, ballooned (floor 8 GiB) |
| Load during the runs | 0.03–1.05 (one-minute average) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Library            | `golang.org/x/crypto` v0.55.0 |
| Reference arm      | `internal/service/crypto/pbkdf2pw` (same repo, same run) |
| Shipped policy     | `m=19456` KiB, `t=2`, `p=1`, 128-bit salt, 256-bit digest |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `f6082f7` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, × 3 processes |

## Results

One of the three processes, verbatim. Each calibration row names the parameter
it configured, so the raw block states what it measured.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/third-party/x-crypto/argon2id
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkHash-8               	      33	  35286486 ns/op	19927052 B/op	      35 allocs/op
BenchmarkHash-8               	      36	  33486216 ns/op	19926939 B/op	      34 allocs/op
BenchmarkHash-8               	      34	  34289441 ns/op	19926904 B/op	      36 allocs/op
BenchmarkVerify-8             	      33	  34796739 ns/op	19927855 B/op	      40 allocs/op
BenchmarkVerify-8             	      36	  34428990 ns/op	19927934 B/op	      41 allocs/op
BenchmarkVerify-8             	      36	  33674460 ns/op	19927829 B/op	      39 allocs/op
BenchmarkVerifyWrong-8        	      36	  33751409 ns/op	19927835 B/op	      40 allocs/op
BenchmarkVerifyWrong-8        	      34	  33616555 ns/op	19927905 B/op	      40 allocs/op
BenchmarkVerifyWrong-8        	      31	  33497159 ns/op	19927863 B/op	      40 allocs/op
BenchmarkVerifyMalformed-8    	10424280	       115.2 ns/op	      64 B/op	       1 allocs/op
BenchmarkVerifyMalformed-8    	10505308	       113.4 ns/op	      64 B/op	       1 allocs/op
BenchmarkVerifyMalformed-8    	10789893	       116.2 ns/op	      64 B/op	       1 allocs/op
BenchmarkNeedsRehash-8        	  438106	      2739 ns/op	     272 B/op	      11 allocs/op
BenchmarkNeedsRehash-8        	  455251	      2796 ns/op	     272 B/op	      11 allocs/op
BenchmarkNeedsRehash-8        	  458676	      2719 ns/op	     272 B/op	      11 allocs/op
BenchmarkBareIDKey-8          	      33	  34814207 ns/op	19925231 B/op	      23 allocs/op
BenchmarkBareIDKey-8          	      34	  33657099 ns/op	19925161 B/op	      23 allocs/op
BenchmarkBareIDKey-8          	      34	  33732687 ns/op	19925146 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=8MiB-8         	      93	  13949075 ns/op	 8390804 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=8MiB-8         	      93	  14324372 ns/op	 8390787 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=8MiB-8         	      85	  13529031 ns/op	 8390859 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=16MiB-8        	      40	  28201636 ns/op	16779436 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=16MiB-8        	      40	  27712727 ns/op	16779427 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=16MiB-8        	      43	  27656884 ns/op	16779450 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=19MiB-shipped-8         	      34	  33512158 ns/op	19925136 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=19MiB-shipped-8         	      36	  33224426 ns/op	19925222 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=19MiB-shipped-8         	      33	  33602244 ns/op	19925213 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=32MiB-8                 	      20	  56017288 ns/op	33556670 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=32MiB-8                 	      19	  56799434 ns/op	33556607 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=32MiB-8                 	      19	  57382458 ns/op	33556731 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=64MiB-8                 	       9	 131507770 ns/op	67111329 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=64MiB-8                 	       9	 123311589 ns/op	67111139 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=64MiB-8                 	       9	 123829544 ns/op	67111020 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=128MiB-8                	       4	 310369462 ns/op	134220020 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=128MiB-8                	       4	 262054607 ns/op	134219872 B/op	      23 allocs/op
BenchmarkCalibrateMemory/m=128MiB-8                	       4	 261991102 ns/op	134220168 B/op	      24 allocs/op
BenchmarkCalibrateTime/t=1-8                       	      64	  18427338 ns/op	19924840 B/op	      15 allocs/op
BenchmarkCalibrateTime/t=1-8                       	      63	  17821207 ns/op	19924891 B/op	      15 allocs/op
BenchmarkCalibrateTime/t=1-8                       	      62	  18177230 ns/op	19924888 B/op	      15 allocs/op
BenchmarkCalibrateTime/t=2-shipped-8               	      33	  33586090 ns/op	19925145 B/op	      23 allocs/op
BenchmarkCalibrateTime/t=2-shipped-8               	      33	  35734888 ns/op	19925159 B/op	      23 allocs/op
BenchmarkCalibrateTime/t=2-shipped-8               	      34	  33516134 ns/op	19925122 B/op	      23 allocs/op
BenchmarkCalibrateTime/t=3-8                       	      22	  50080925 ns/op	19925505 B/op	      31 allocs/op
BenchmarkCalibrateTime/t=3-8                       	      21	  49868056 ns/op	19925372 B/op	      31 allocs/op
BenchmarkCalibrateTime/t=3-8                       	      21	  49663177 ns/op	19925597 B/op	      31 allocs/op
BenchmarkCalibrateTime/t=4-8                       	      16	  65750374 ns/op	19925644 B/op	      39 allocs/op
BenchmarkCalibrateTime/t=4-8                       	      18	  65633537 ns/op	19925797 B/op	      39 allocs/op
BenchmarkCalibrateTime/t=4-8                       	      16	  64837466 ns/op	19925755 B/op	      39 allocs/op
BenchmarkCalibrateTime/t=6-8                       	      12	  95873096 ns/op	19926349 B/op	      55 allocs/op
BenchmarkCalibrateTime/t=6-8                       	      12	  98094852 ns/op	19926506 B/op	      56 allocs/op
BenchmarkCalibrateTime/t=6-8                       	      12	  96408434 ns/op	19926248 B/op	      55 allocs/op
BenchmarkCalibrateThreads/p=1-shipped-8            	      34	  33560359 ns/op	19925114 B/op	      23 allocs/op
BenchmarkCalibrateThreads/p=1-shipped-8            	      36	  33268460 ns/op	19925088 B/op	      23 allocs/op
BenchmarkCalibrateThreads/p=1-shipped-8            	      33	  34166079 ns/op	19925088 B/op	      23 allocs/op
BenchmarkCalibrateThreads/p=2-8                    	      52	  21350386 ns/op	19926323 B/op	      33 allocs/op
BenchmarkCalibrateThreads/p=2-8                    	      56	  22301784 ns/op	19926394 B/op	      33 allocs/op
BenchmarkCalibrateThreads/p=2-8                    	      54	  21156277 ns/op	19926406 B/op	      33 allocs/op
BenchmarkCalibrateThreads/p=4-8                    	      87	  15497719 ns/op	19928923 B/op	      54 allocs/op
BenchmarkCalibrateThreads/p=4-8                    	      75	  15945986 ns/op	19928710 B/op	      53 allocs/op
BenchmarkCalibrateThreads/p=4-8                    	      76	  15204867 ns/op	19928767 B/op	      53 allocs/op
BenchmarkCalibrateThreads/p=8-8                    	     100	  12074254 ns/op	19933338 B/op	      93 allocs/op
BenchmarkCalibrateThreads/p=8-8                    	      99	  12498402 ns/op	19933227 B/op	      93 allocs/op
BenchmarkCalibrateThreads/p=8-8                    	      86	  12808484 ns/op	19933230 B/op	      93 allocs/op
BenchmarkPBKDF2Hash-8                              	       8	 143214992 ns/op	    1343 B/op	      21 allocs/op
BenchmarkPBKDF2Hash-8                              	       7	 143149136 ns/op	    1307 B/op	      20 allocs/op
BenchmarkPBKDF2Hash-8                              	       7	 145538275 ns/op	    1307 B/op	      20 allocs/op
BenchmarkPBKDF2Verify-8                            	       8	 140011338 ns/op	     932 B/op	      14 allocs/op
BenchmarkPBKDF2Verify-8                            	       8	 140886912 ns/op	     932 B/op	      14 allocs/op
BenchmarkPBKDF2Verify-8                            	       8	 140325012 ns/op	     932 B/op	      14 allocs/op
PASS
ok  	github.com/kitsunium/sdk/third-party/x-crypto/argon2id
```
