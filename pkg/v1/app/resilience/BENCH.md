<!-- generated from pkg/v1/app/resilience/resilience_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/app/resilience/` to refresh -->
# Benchmarks — `pkg/v1/app/resilience`

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

## The retry budget costs a few nanoseconds, and allocates nothing ([issue 475](https://github.com/kitsunium/sdk/issues/475))

A run of its own, beside a fresh baseline — its envelope is below, and the
2026-09-10 envelope further down covers the table above, not this one:

| policy | ns/op | B/op | allocs | over baseline |
|---|---:|---:|---:|---:|
| baseline (no policy) | 2.821 | 0 | 0 | — |
| `Retry`, first attempt succeeds | 6.882 | 0 | **0** | +4.1 ns |
| `Retry` with a `RetryBudget`, first attempt succeeds | 9.102 | 0 | **0** | +6.3 ns |
| `RetryBudget` alone, 8 goroutines: failure + question + success | 49.33 | 0 | **0** | — |

A budget adds about 2 ns to the happy path — the success it records on a full
bucket is one atomic load that writes nothing — and the bucket stays lock-free
under contention: three operations on one shared counter from every core cost
under 50 ns together, with no allocation.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P, 8 cores |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | feat/resilience-budget-retryafter |
| Git commit         | 95503e90 (the budget's code as merged; later commits touch docs only) |
| Generated (UTC)    | 2026-10-21 |
| Bench wall-clock   | `go test -run='^$' -bench='Baseline\|Retry_FirstAttempt\|WithBudget\|Budget_Contended' -benchmem -benchtime=1s`, single run |

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/app/resilience
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkBaseline_NoPolicy-8                       	420117145	         2.821 ns/op	       0 B/op	       0 allocs/op
BenchmarkRetry_FirstAttemptSucceeds-8              	174201987	         6.882 ns/op	       0 B/op	       0 allocs/op
BenchmarkRetry_WithBudget_FirstAttemptSucceeds-8   	131862070	         9.102 ns/op	       0 B/op	       0 allocs/op
BenchmarkRetryBudget_Contended-8                   	21669429	        49.33 ns/op	       0 B/op	       0 allocs/op
```

## The breaker's window costs two clock reads, and allocates nothing ([issue 480](https://github.com/kitsunium/sdk/issues/480))

A run of its own, `-count=5`, the median of each row; the consecutive rows were
run the same way on `origin/main` (`0d4dcef6`, before the window) on the same
machine, minutes apart:

| breaker | before #480 | after #480 | B/op | allocs |
|---|---:|---:|---:|---:|
| consecutive (`Window: nil`), closed | 46.26 | 48.42 | 0 | **0** |
| consecutive, open | 67.00 | 65.92 | 0 | **0** |
| `Window` set, closed | — | 195.7 | 0 | **0** |
| `Window` set, closed, with an `OnStateChange` | — | 194.8 | 0 | **0** |
| `State()` (a readiness probe, a scrape) | — | 109.4 | 0 | **0** |

**A nil Window costs 2 ns more than before, and nothing in allocations**: the
closed path reads no clock and defers nothing — the trial and the window run
on paths of their own, which also took the deferred release off it — and what
remains is the check for a window and for a change of phase owed to a
listener; the five runs of each column do not overlap (46.06–46.34 against
48.26–48.56), so the 2 ns are real, and the open row moved 1 ns the other way.
**A window allocates nothing per call**: its slots are allocated once, at
construction.

Its 147 ns over the consecutive breaker are, almost all, **two reads of the
clock** — the start and the end of the call, which the slow-call criterion
needs: `time.Now()` costs 70.8 ns on this VM, whose clock source is
`kvm-clock` (`BenchmarkNow`, a three-line benchmark outside the tree, three
runs at 70.6 to 70.9 ns), against about 20 ns on bare metal with a TSC. The
window's own work — the slot, the totals, a ratio read only when one could
have risen — is the few nanoseconds left. `State()` is one lock and one clock
read (it moves the window to now). A listener costs nothing while the phase
holds.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P, 8 cores |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | feat/resilience-breaker-window |
| Generated (UTC)    | 2026-10-21 |
| Bench wall-clock   | `go test -run='^$' -bench='CircuitBreaker' -benchmem -benchtime=1s -count=5` |

```
BenchmarkCircuitBreaker_Closed-8                   	24787677	        48.56 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Closed-8                   	24748015	        48.56 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Closed-8                   	24836856	        48.27 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Closed-8                   	24900913	        48.42 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Closed-8                   	24787358	        48.26 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed-8            	 6203898	       195.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed-8            	 6216579	       196.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed-8            	 6213520	       195.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed-8            	 6144550	       195.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed-8            	 6184328	       194.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed_Listened-8   	 6026143	       200.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed_Listened-8   	 6189811	       194.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed_Listened-8   	 6190444	       194.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed_Listened-8   	 6171469	       194.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Window_Closed_Listened-8   	 6191280	       193.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_State-8                    	11068659	       109.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_State-8                    	11077640	       108.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_State-8                    	10500126	       111.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_State-8                    	10789452	       109.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_State-8                    	10969453	       110.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Open-8                     	18201648	        65.87 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Open-8                     	18205429	        66.00 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Open-8                     	18234022	        65.98 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Open-8                     	18210210	        65.92 ns/op	       0 B/op	       0 allocs/op
BenchmarkCircuitBreaker_Open-8                     	18243685	        65.76 ns/op	       0 B/op	       0 allocs/op
```

## A wait that is not taken costs nothing, and the throttle is two clock reads ([ADR 0184](../../../../docs/adr/0184-a-wait-is-bounded-a-restart-gives-up-and-a-client-throttles-itself.md), [issue 482](https://github.com/kitsunium/sdk/issues/482))

A run of its own, `-count=5`, the median of each row; the "before" column of
the rate limiter is the admitted path of `origin/main` (`f36c4583`), swapped
into the same tree and run on the same machine minutes earlier:

| policy | before #482 | after #482 | B/op | allocs |
|---|---:|---:|---:|---:|
| `RateLimiter`, admitted | 115.2 | 117.4 | 0 | **0** |
| `RateLimiter` with a `MaxWait`, admitted | — | 117.7 | 0 | **0** |
| `Bulkhead`, uncontended | — | 64.33 | 0 | **0** |
| `BulkheadWait`, uncontended (a slot free) | — | 68.16 | 0 | **0** |
| `AdaptiveThrottle`, healthy dependency | — | 189.7 | 0 | **0** |

**A wait that is not taken is not paid for.** The limiter's admitted path
gained the `refill` call `take` now shares with `giveBack` and one branch on
the wait: 2 ns, and a `MaxWait` adds nothing more while a token is there. The
bulkhead that queues pays 4 ns over the rejecting one on a free slot — a second
`select` it never enters — and nothing allocates on either. Only a call that
actually waits arms a timer on the clock.

**The throttle is two clock reads and two locks**: the request is counted at
admission and the accept when the call returns, each in the slot of its own
instant, so it reads `time.Now()` twice — about 70 ns each on this VM's
`kvm-clock` (see the breaker's window above) — and the probability is zero on a
healthy dependency, so `Random` is never drawn.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P, 8 cores |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | feat/resilience-bounds |
| Generated (UTC)    | 2026-10-21 |
| Bench wall-clock   | `go test -run='^$' -bench='RateLimiter_Admitted\|Bulkhead\|AdaptiveThrottle' -benchmem -benchtime=1s -count=5` |

```
BenchmarkRateLimiter_Admitted-8               	10284525	       117.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted-8               	10313455	       116.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted-8               	10264378	       117.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted-8               	10262886	       116.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted-8               	10147189	       117.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted_WithMaxWait-8   	10263069	       117.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted_WithMaxWait-8   	10306687	       117.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted_WithMaxWait-8   	 9958446	       121.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted_WithMaxWait-8   	 9879178	       118.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkRateLimiter_Admitted_WithMaxWait-8   	10240167	       117.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkheadWait_Uncontended-8   	17759640	        68.12 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkheadWait_Uncontended-8   	17725741	        67.79 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkheadWait_Uncontended-8   	17667961	        68.16 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkheadWait_Uncontended-8   	17499728	        68.34 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkheadWait_Uncontended-8   	17441956	        68.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkAdaptiveThrottle_Healthy-8   	 6341730	       191.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkAdaptiveThrottle_Healthy-8   	 6268219	       191.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkAdaptiveThrottle_Healthy-8   	 6316921	       189.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkAdaptiveThrottle_Healthy-8   	 6313269	       189.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkAdaptiveThrottle_Healthy-8   	 6344408	       189.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Uncontended-8       	18519628	        64.29 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Uncontended-8       	18679489	        64.33 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Uncontended-8       	18190588	        64.79 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Uncontended-8       	18395871	        64.33 ns/op	       0 B/op	       0 allocs/op
BenchmarkBulkhead_Uncontended-8       	18672102	        64.88 ns/op	       0 B/op	       0 allocs/op
```

## What is NOT measured here

`Hedge` has no benchmark. It duplicates the operation after a delay, so any
honest measurement of it waits on the wall clock, and a benchmark that sleeps
reports the sleep. Its cost is one extra goroutine plus one extra execution of
the caller's operation — which is why ADR 0031 makes `HedgeConfig.Idempotent`
an assertion the caller writes in code.

Retry with actual retries is likewise absent: it waits `BaseDelay` between
attempts, so the number would be the backoff schedule, which the caller chose.

A `RateLimited` rejection now allocates — the delay it carries for
`RetryAfter` rides a wrap of the sentinel (#476) — and is not benchmarked: the
admitted path, measured above, is unchanged.

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
| Git commit         | 0bfbf60 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/app/resilience
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
ok  	github.com/kitsunium/sdk/pkg/v1/app/resilience	13.246s
```
