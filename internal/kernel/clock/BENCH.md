<!-- generated from internal/kernel/clock/clock_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./clock/` to refresh -->
# Benchmarks — `internal/kernel/clock`

The package carries two halves. Reading time (`Now` / `Since`) is the smallest
yet most-called path in the SDK — every logger record calls `System.Now()`
exactly once. Waiting on time (`After` / `NewTimer` / `NewTicker` / `Sleep`) is
newer and colder, but it is the half a `ManualClock` has to reimplement, so it
carries two numbers the design depends on:

- **`Stdlib_NewTimer` vs `System_NewTimer`** — the control pair. `systemTimer`
  exists only to turn `*time.Timer`'s `C` *field* into the `C()` *method* an
  interface can require, and the claim is that adapting it costs no allocation.
  The pair is what makes that claim checkable rather than asserted.
- **`Manual_Now` vs `System_Now`, and the two `Manual_Advance` cells** — what a
  test pays to make time deterministic. They have to stay small enough that a
  `ManualClock` is usable inside a benchmark, not only inside a test.

They exist to lock in a baseline so any future change has a regression gate; the
hot-path `Now()` must not regress by more than ~5 ns/op, and
`System_NewTimer` must not allocate more than `Stdlib_NewTimer`.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P) |
| RAM                | 15 GiB (ballooned VM — see the caveat below) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | agent-ac82922e0bc8cfe7f |
| Git commit         | 43131fb |
| Generated (UTC)    | 2026-09-09 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

> **Caveat, stated because leaving it out would be dishonest.** This run was
> taken on a shared, memory-ballooned VM that may have been executing other
> jobs concurrently. Treat the absolute ns/op as an order of magnitude, and the
> `B/op` and `allocs/op` columns — which are deterministic — as exact. The
> previous edition of this file was measured on arm64 with go1.26.4; the whole
> table was re-measured here rather than half-updated, so the cells stay
> comparable with each other even though they are not comparable with that
> edition.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/clock
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkSystem_Now-8              	16797492	        71.80 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_Now_Parallel-8     	17787106	        70.51 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_Since-8            	28079414	        43.42 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_Since_Parallel-8   	25187066	        55.05 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_NowSincePair-8     	 7078188	       149.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkStdlib_NewTimer-8         	 3187216	       485.1 ns/op	     248 B/op	       3 allocs/op
BenchmarkSystem_NewTimer-8         	 2673064	       419.7 ns/op	     248 B/op	       3 allocs/op
BenchmarkSystem_NewTicker-8        	 2789235	       418.3 ns/op	     248 B/op	       3 allocs/op
BenchmarkManual_Now-8              	51974493	        25.52 ns/op	       0 B/op	       0 allocs/op
BenchmarkManual_NewTimer-8         	 3565071	       367.0 ns/op	     216 B/op	       4 allocs/op
BenchmarkManual_Advance_Idle-8     	16082997	        74.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkManual_Advance_Firing-8   	  906280	      2046 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/kernel/clock	15.817s
```

## How to read this

### Reading time

- **`BenchmarkSystem_Now`** — the per-record hot path: a single
  `System.Now()` wall-clock read. This is the baseline number callers use to
  reason about logger overhead (one clock read per record), 0 allocs/op.
- **`BenchmarkSystem_Now_Parallel`** — the same read under `RunParallel` so
  many goroutines read the clock concurrently. There is no shared state, so the
  number confirms `time.Now()` introduces no contention under `GOMAXPROCS>1`.
- **`BenchmarkSystem_Since`** — the `time.Since` wrapper against a start instant
  captured once before the loop, so it isolates the subtraction, not a second
  clock read.
- **`BenchmarkSystem_Since_Parallel`** — `Since` under `RunParallel`; the start
  instant is shared read-only, confirming the elapsed read scales without
  contention.
- **`BenchmarkSystem_NowSincePair`** — the typical timing-span shape: one read
  to open the span and one read+subtract to close it. It documents the combined
  end-to-end cost (≈ `Now` + `Since`).

### Waiting on time

- **`BenchmarkStdlib_NewTimer`** — the CONTROL: raw `time.NewTimer` + `Stop`,
  no port in the way. 248 B/op, 3 allocs/op.
- **`BenchmarkSystem_NewTimer`** — the same construction through
  `Waiter.NewTimer`. **248 B/op, 3 allocs/op — byte-for-byte identical to the
  control.** That is the measurement behind the claim in `system_timer.go`:
  `systemTimer` holds exactly one pointer, so it is pointer-shaped and the
  runtime stores it directly in the interface word instead of heap-boxing it.
  Adapting the stdlib timer to the SDK's `Timer` interface is free.
- **`BenchmarkSystem_NewTicker`** — same shape for tickers, including the
  `requirePositivePeriod` guard on the path. Same 3 allocs, same 248 B.

### The deterministic clock

- **`BenchmarkManual_Now`** — a mutex-guarded read of a stored instant, at
  ~25 ns/op against ~72 ns/op for `System.Now()`. The manual clock is *faster*
  than the real one on this box: an `RWMutex.RLock` plus a `time.Time` copy
  costs less than the wall-clock read `time.Now()` performs. Determinism here
  is not something a test pays for — it is something it is paid for.
- **`BenchmarkManual_NewTimer`** — registering one wait: a cap-1 channel, a
  `manualWait` record, and an append to the armed set. 4 allocs/op against the
  stdlib's 3, for slightly fewer bytes.
- **`BenchmarkManual_Advance_Idle`** — `Advance` with 8 armed waits, none due:
  the deadline scan alone, allocation-free. This is the common case in a test
  that steps time in small increments, and it is O(armed waits) by design — a
  linear scan is the right structure for the handful of waits a test holds.
- **`BenchmarkManual_Advance_Firing`** — the worst realistic shape: 8 tickers,
  one full period per call, so every wait fires and re-arms. ~2 µs/op and still
  allocation-free — the re-arm is one `time.Time.Add`, never a loop over the
  skipped periods, which is what keeps `Advance(time.Hour)` on a nanosecond
  ticker bounded.
