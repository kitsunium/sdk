<!-- generated from pkg/v1/health/health_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/health/` to refresh -->
# Benchmarks — `pkg/v1/health`

Startup, readiness and liveness (ADR 0060). An orchestrator polls these every
few seconds per replica, so the absolute cost matters less than **how it grows**
— a readiness probe runs every registered check, and a service that adds a
dependency adds it to every probe from then on.

## Readiness is linear in the number of checks, at ~4.3 µs each

| checks | ns/op | B/op | allocs |
|---:|---:|---:|---:|
| 1 | 7 533 | 824 | 14 |
| 8 | 48 330 | 6 486 | 91 |
| 32 | 134 373 | 26 005 | 355 |

Roughly **4.3 µs and 11 allocations per check**, for a check whose whole body is
`return nil`. That looks like a lot until the profile says what it is.

## What the per-check cost buys, from the profile

`go tool pprof -sample_index=alloc_objects` on the 32-check probe:

| | share of allocated objects |
|---|---:|
| `time.NewTimer` (+ `time.newTimer`) | **28.9 %** |
| `entry.claim` — the in-flight dedup | 18.4 % |
| `context.WithCancel` (+ `withCancel`) | 15.5 % |
| `context.WithoutCancel` | 10.7 % |
| `sync.WaitGroup.Go` — one goroutine per check | 8.4 % |

That is **one budgeted context and one goroutine, per check, per probe** — which
is the design, not overhead on top of it. ADR 0060 gives each check its own
timeout so the first dependency that will not answer cannot spend everyone
else's, the same rule ADR 0050 applies to `lifecycle`'s shutdown budget. A
context with a deadline costs a timer and a cancel; `pkg/v1/resilience/BENCH.md`
measures that combination independently at 993 ns and 4 allocations.

**Nothing here is optimised, and that is the conclusion rather than an
omission.** Removing the per-check timer would mean removing the per-check
budget, which is the property the domain exists to provide. 134 µs once every
few seconds is not a cost worth trading it for.

## Liveness is flat, and that is ADR 0060's whole argument in one row

`ProbeLiveness_32` is **7 644 ns / 13 allocs** against readiness's 134 373 ns /
355 allocs **on the same registry**.

The gap is not an optimisation. A liveness check is a `SelfCheck` —
`func() error`, with **no context** — so it cannot reach a dependency, and this
registry has exactly one of them however many readiness checks it carries. ADR
0060 makes liveness and readiness take *different types* precisely so that
"liveness pings the database" needs a closure that visibly discards the
deadline, and this row is what that decision saves: a liveness probe stays flat
while readiness grows.

Which matters more than the microseconds, because liveness failing is what
restarts the process.

## `MaxAge` is worth 5×, and it is the answer for a large registry

| 8 checks | ns/op | allocs |
|---|---:|---:|
| every probe runs every check | 48 330 | 91 |
| within a 10 s `MaxAge` window | **9 709** | **19** |

**5.0× faster, 4.8× fewer allocations.** A registry polled faster than its
checks can meaningfully change should carry a `MaxAge`, and this is the number
that says how much that is worth.

The ceiling is enforced rather than suggested: a `MaxAge` above `MaxCacheAge` is
**REFUSED** at registration with `STALE_CACHE_WINDOW`, not clamped. Writing this
benchmark tripped over it — a one-hour window was refused outright — which is
the guard working: a health verdict an hour old is not a health verdict, and
clamping would have answered a question the caller did not ask.

## The endpoint

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `/readyz`, 8 checks, terse | 57 314 | 7 679 | 105 |
| `/readyz`, 8 checks, `Detail: true` | 70 420 | 9 079 | 107 |

Detail costs **23 %** and two allocations — it decides whether the per-check
body is computed at all, so the switch is real work rather than a rendering
flag. Terse is 9 µs above the bare probe, which is the HTTP response.

`Worst` — the status fold, run once per check per probe — is 2.973 ns and
allocates nothing.

## Reproducibility envelope

> **Numbers vary across machines.** The allocation column is exact and does not
> vary; the nanoseconds do. The RATIOS this report asserts — linear-in-checks,
> liveness-flat, cache-worth-5× — held on every run.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 9a321a5 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/health
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkProbeReadiness_1-8           	  165529	      7533 ns/op	     824 B/op	      14 allocs/op
BenchmarkProbeReadiness_8-8           	   24334	     48330 ns/op	    6486 B/op	      91 allocs/op
BenchmarkProbeReadiness_32-8          	    9576	    134373 ns/op	   26005 B/op	     355 allocs/op
BenchmarkProbeLiveness_32-8           	  153914	      7644 ns/op	     823 B/op	      13 allocs/op
BenchmarkHandler_Readiness_Terse-8    	   21012	     57314 ns/op	    7679 B/op	     105 allocs/op
BenchmarkHandler_Readiness_Detail-8   	   16592	     70420 ns/op	    9079 B/op	     107 allocs/op
BenchmarkProbeReadiness_Cached-8      	  127198	      9709 ns/op	    1552 B/op	      19 allocs/op
BenchmarkWorst-8                      	403342810	         2.973 ns/op	       0 B/op	       0 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/health	9.709s
```
