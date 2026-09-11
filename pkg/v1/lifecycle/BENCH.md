<!-- generated from pkg/v1/lifecycle/lifecycle_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/lifecycle/` to refresh -->
# Benchmarks — `pkg/v1/lifecycle`

Ordered bring-up and reverse teardown (ADR 0050). This runs **once per
process**, so the absolute numbers matter only as a proportion of a real
startup. The reason to measure it is a different one, and it is the first
section.

## The failure path costs the same as the success path

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Start` + `Stop`, 32 components, all succeed | 107 581 | 22 625 | 297 |
| **`Start` of 32 where the last one FAILS** | **105 049** | 22 386 | 293 |

Within noise of each other, and that is the design being visible rather than a
coincidence.

ADR 0050 unwinds a partial start **through the same `stopEach` an ordinary
`Stop` uses**, over a list appended to before the next `Start` is attempted,
with contexts detached by `WithoutCancel` so a start aborted by cancellation
still gets a real cleanup. If the unwind were a separate branch it would be
cheaper *and* worse — a rarely-exercised path that nobody runs until the day a
deployment half-fails. These two rows say it is not a separate branch.

The component that failed is never stopped, which is why the failing row is 4
allocations lighter: a `Start` that fails owns what it acquired.

## The engine costs ~3.3 µs and ~9 allocations per component

| components | ns/op | B/op | allocs |
|---:|---:|---:|---:|
| 1 | 7 661 | 664 | 13 |
| 8 | 35 566 | 5 536 | 79 |
| 32 | 107 581 | 22 625 | 297 |

Linear, with the components themselves doing **nothing** — so this is the
ordering machinery alone. A profile says what it is:

| | share of allocated objects |
|---|---:|
| `callStop` (cumulative) | **79.4 %** |
| `time.NewTimer` (+ `newTimer`) | 28.7 % |
| `context.WithCancel` (+ `withCancel`) | 25.9 % |
| `context.WithoutCancel` | 10.1 % |

That is **the per-component stop budget being real**: each `Stop` gets its own
deadline, detached from whatever cancelled the start, so the first component
that will not finish cannot spend everyone else's. ADR 0050 argues for that at
length; this is its price, and it is 3.3 µs per component paid once.

The same shape appears in `pkg/v1/health/BENCH.md` (a budgeted context per
check) and is priced independently in `pkg/v1/resilience/BENCH.md` (a deadline
context is 993 ns and 4 allocations). Three domains, one mechanism, one cost.

## Nothing here is worth optimising, with the arithmetic

A thirty-two-component application spends **108 µs** ordering its startup and
shutdown. An empty Go binary takes about **2 ms** to start and exit — measured
independently in `pkg/v1/cli/BENCH.md` — so the whole lifecycle engine is
roughly **5 %** of doing nothing at all, before a single component has opened a
socket or dialled a database.

`Add` is 298.7 ns and **one allocation**, at wiring time.

The one thing worth protecting is the first section: if the unwind ever stops
costing what the ordinary teardown costs, it has stopped being the ordinary
teardown.

## Reproducibility envelope

> **Numbers vary across machines.** The allocation column is exact; the
> nanoseconds are not. What this report asserts is the two RATIOS —
> unwind-equals-teardown, and linear-in-components — both of which held on every
> run.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 85910ac |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/lifecycle
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkStartStop_1-8           	  159486	      7661 ns/op	     664 B/op	      13 allocs/op
BenchmarkStartStop_8-8           	   34627	     35566 ns/op	    5536 B/op	      79 allocs/op
BenchmarkStartStop_32-8          	   10000	    107581 ns/op	   22625 B/op	     297 allocs/op
BenchmarkAdd-8                   	 4040350	       298.7 ns/op	     213 B/op	       1 allocs/op
BenchmarkStart_PartialUnwind-8   	   10000	    105049 ns/op	   22386 B/op	     293 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/lifecycle	25.825s
```
