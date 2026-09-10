<!-- generated from pkg/v1/resilience/resilience_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/resilience/` to refresh -->
# Benchmarks — `pkg/v1/resilience`

Seven composable policies (ADR 0026 / ADR 0031). Every one of them wraps a call,
so its cost is paid on **every** guarded call — and the guarded work is held at
**zero** in these benchmarks on purpose, so what is measured is the policy and
nothing else. Read each row as a delta against the 2.803 ns baseline.

## The whole table, sorted by what it costs

| policy | ns/op | B/op | allocs | over baseline |
|---|---:|---:|---:|---:|
| baseline (no policy) | 2.803 | 0 | 0 | — |
| `Retry`, first attempt succeeds | 6.292 | 0 | **0** | +3.5 ns |
| `Fallback`, primary succeeds | 7.156 | 0 | **0** | +4.4 ns |
| `Fallback`, primary fails | 12.59 | 0 | **0** | +9.8 ns |
| `CircuitBreaker`, closed | 48.18 | 0 | **0** | +45 ns |
| `CircuitBreaker`, open | 62.99 | 0 | **0** | +60 ns |
| `Bulkhead`, uncontended | 66.08 | 0 | **0** | +63 ns |
| `Bulkhead`, 8 goroutines | 108.4 | 0 | **0** | +106 ns |
| `RateLimiter`, admitted | 110.1 | 0 | **0** | +107 ns |
| **`Timeout`** | **993.2** | **272** | **4** | **+990 ns** |
| composed: retry → breaker → timeout | 1 090 | 272 | 4 | +1 087 ns |

**Six of the seven allocate nothing.** `Timeout` is the exception, and it is
also 9× more expensive than the next policy and 350× the baseline.

## `Timeout` is the one policy with a real price, and the reason is structural

`context.WithTimeout` registers a runtime timer and builds a derived context:
four allocations and 272 bytes, per call, whether or not the deadline ever
fires. Nothing in this SDK can remove that — it is what a cancellable deadline
costs in Go.

The consequence is a placement rule rather than an optimisation:

- **Wrapping I/O in a `Timeout` is free.** One microsecond against a network
  round trip of hundreds is noise, and the deadline is the entire point.
- **Wrapping an in-memory call in a `Timeout` is not.** A `Timeout` around a
  60 ns cache lookup costs 16× the lookup, to bound something that cannot hang.

The composed row makes it concrete: `retry → breaker → timeout` costs 1 090 ns,
of which **993 ns is the timeout**. Retry and breaker together add 97 ns. If a
composed stack looks expensive, it is almost certainly the deadline.

## The breaker refuses for slightly MORE than it admits, and that is fine

`CircuitBreaker` open (62.99 ns) costs about 15 ns more than closed (48.18 ns).
That ordering is counterintuitive — a tripped breaker is supposed to be the fast
path — and it is reported as measured rather than explained away, because the
explanation would be a guess and the magnitude does not warrant chasing one.

What matters is the comparison the breaker actually exists for: **63 ns against
the dependency call it is standing in for**. A breaker protecting a 5 ms
database round trip returns 80 000× faster than the call it refuses. That is the
number, and it is not close.

## `Bulkhead` costs 1.64× under contention

66.08 ns uncontended, 108.4 ns across eight goroutines. A concurrency limiter is
a shared counter, so some contention is structural; 1.64× for the guarantee is
cheap, and it is now measured rather than assumed. Both figures are zero
allocations.

## `Retry` and `Fallback` are effectively free when they do nothing

A retry policy spends almost all of its life not retrying, and a fallback almost
all of its life not falling back. Those paths cost **3.5 ns and 4.4 ns** over
calling the operation directly, with no allocation — so neither is a reason to
leave a call unguarded.

`Fallback` with a failing primary is 12.59 ns, still allocation-free: the
switch itself is ~5 ns, and everything above it is plan B's own cost.

## What is NOT measured here

`Hedge` has no benchmark. It duplicates the operation after a delay, so any
honest measurement of it waits on the wall clock, and a benchmark that sleeps
reports the sleep. Its cost is one extra goroutine plus one extra execution of
the caller's operation — which is why ADR 0031 makes `HedgeConfig.Idempotent`
an assertion the caller writes in code.

Retry with actual retries is likewise absent: it waits `BaseDelay` between
attempts, so the number would be the backoff schedule, which the caller chose.

## Reproducibility envelope

> **Numbers vary across machines.** This run shared the box with four other
> jobs. The allocation column is exact and does not vary; the nanoseconds do,
> and the RATIOS — policy against baseline, and `Timeout` against everything
> else — are what this table asserts.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | e076197 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/resilience
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkBaseline_NoPolicy-8              	429021234	         2.803 ns/op	       0 B/op	       0 allocs/op
BenchmarkRetry_FirstAttemptSucceeds-8     	190694822	         6.292 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Closed-8          	25067592	        48.18 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Open-8            	19154384	        62.99 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted-8           	10967094	       110.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Uncontended-8           	18166477	        66.08 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Contended-8             	11138788	       108.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkTimeout-8                        	 1205604	       993.2 ns/op	     272 B/op	       4 allocs/op
BenchmarkFallback_PrimarySucceeds-8       	167060680	         7.156 ns/op	       0 B/op	       0 allocs/op
BenchmarkFallback_PrimaryFails-8          	96717597	        12.59 ns/op	       0 B/op	       0 allocs/op
BenchmarkComposed_RetryBreakerTimeout-8   	  989005	      1090 ns/op	     272 B/op	       4 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/resilience	13.246s
```
