<!-- generated from internal/kernel/clock/clock_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./clock/` to refresh -->
# Benchmarks — `internal/kernel/clock`

The thinnest kernel package: a `Clock` interface (`Now`/`Since`) and the
`System` value backed by the stdlib wall clock. Every logger record calls
`System.Now()` exactly once, so this is the smallest yet most-called function
in the SDK. These benchmarks isolate that hot-path read (`System.Now`), the
elapsed-duration wrapper (`System.Since`), their parallel variants — to confirm
the stdlib reads introduce no contention under `GOMAXPROCS>1` — and the typical
record-a-start-then-measure-elapsed pair. They exist to lock in a baseline
number so any future change (monotonic clock, mock abstraction) has a regression
gate; the hot-path `Now()` must not regress by more than ~5 ns/op.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 11 GiB |
| OS / kernel        | Linux 6.12.72-linuxkit |
| Architecture       | arm64 |
| Go toolchain       | go1.26.4 linux/arm64 |
| Git branch         | feat/issue-19-kernel-clock-bench |
| Git commit         | f3b1610 |
| Generated (UTC)    | 2026-06-19 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: arm64
pkg: github.com/kitsunium/sdk/internal/kernel/clock
BenchmarkSystem_Now-8              	26297334	        46.11 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_Now_Parallel-8     	17230155	        69.77 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_Since-8            	46109139	        24.71 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_Since_Parallel-8   	23285955	        52.94 ns/op	       0 B/op	       0 allocs/op
BenchmarkSystem_NowSincePair-8     	14198914	        82.97 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/kernel/clock	6.114s
```

## How to read this

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
