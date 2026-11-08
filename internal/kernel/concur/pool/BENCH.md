<!-- generated from internal/kernel/concur/pool/pool_bench_test.go — run `cd internal && go test -run='^$' -bench=. -benchmem -cpu 1,4,8 -count 5 ./kernel/concur/pool/` to refresh -->
# Benchmarks — `internal/kernel/concur/pool`

The bounded worker pool of #804 (ADR 0216). These benchmarks defend its
contract — a `Submit` that allocates nothing in steady state — and set it
beside the one-shot fan-out it is the long-lived alternative to.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 vCPU) |
| OS / kernel | Linux 6.12 (Debian 13) |
| Architecture | amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Git base | `42143bf47` + the kernel lot |
| Generated | 2026-11-03 |
| Bench wall-clock | `-benchmem -count 5 -cpu 1,4,8`, median of 5 runs |

## Results

| Benchmark | `-cpu 1` | `-cpu 4` | `-cpu 8` | B/op | allocs/op |
|---|---|---|---|---|---|
| `Submit_Steadystate` | 95.4 ns | 237.1 ns | 235.9 ns | 0 | 0 |
| `Submit_Parallel` | 96.6 ns | 209.7 ns | 197.8 ns | 0 | 0 |
| `SubmitWait_OneTask` | 763 ns | 808 ns | 798 ns | 0 | 0 |
| `GroupGoWait_OneTask` (baseline) | 1 467 ns | 1 873 ns | 1 859 ns | 328 | 5 |
| `Drain_1000` | 99.3 µs | 243.2 µs | 236.7 µs | 10 130 | 12 |

## How to read this

- **`Submit_Steadystate`** — one `Submit` into a running pool of four workers
  and a 1 024-task queue, the task built once (the closure is the caller's).
  **0 allocs/op** is #804's acceptance and the design's budget on
  `(*Pool).Submit` (`perf_fixtures_test.go`, ADR 0165). The time is a channel
  send under a read lock; with more than one P it includes waking a parked
  worker.
- **`SubmitWait_OneTask` against `GroupGoWait_OneTask`** — the same one task,
  waited for: through a running pool it costs no allocation and about 0.8 µs;
  through `group` (`New`, `Go`, `Wait`) a goroutine, a context and 5
  allocations, about 1.9 µs. That difference, per task, is what a service
  running the same work for as long as it lives stops paying.
- **`Drain_1000`** — a pool's whole life around a burst: `New` (the workers,
  their channel, the context: the 12 allocations), 1 000 tasks, `Drain`.
