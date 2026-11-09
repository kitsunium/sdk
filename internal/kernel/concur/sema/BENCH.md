<!-- generated from internal/kernel/concur/sema/sema_bench_test.go — run `cd internal && go test -run='^$' -bench=. -benchmem -cpu 1,8 -count 5 ./kernel/concur/sema/` to refresh -->
# Benchmarks — `internal/kernel/concur/sema`

The weighted semaphore of #812 (ADR 0216), set beside the unit semaphore the
SDK's bulkheads are built on — a buffered `chan struct{}`.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 vCPU) |
| OS / kernel | Linux 6.12 (Debian 13) |
| Architecture | amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Git base | `42143bf47` + the kernel lot |
| Generated | 2026-11-03 |
| Bench wall-clock | `-benchmem -count 5 -cpu 1,8`, median of 5 runs |

## Results

| Benchmark | `-cpu 1` | `-cpu 8` | B/op | allocs/op |
|---|---|---|---|---|
| `Acquire_Uncontended` | 45.2 ns | 45.0 ns | 0 | 0 |
| `ChanBaseline_Uncontended` | 51.8 ns | 55.2 ns | 0 | 0 |
| `Acquire_Parallel` | 42.3 ns | 150.4 ns | 0 | 0 |
| `ChanBaseline_Parallel` | 51.5 ns | 68.9 ns | 0 | 0 |
| `Acquire_Contended_FIFO` | 42.8 ns | 684.1 ns | 143 | 1 |

## How to read this

- **`Acquire_Uncontended`** — an `Acquire(ctx, 1)` and its `Release(1)` on a
  free semaphore: two lock sections. **0 allocs/op and below the channel
  baseline** — #812's acceptance (≤ 2× the `chan struct{}` row) is met, and
  the design's budget on `(*Weighted).Acquire` holds it (ADR 0165).
- **`Acquire_Parallel`** — every P on one semaphore of 64 units: the mutex is
  contended where the channel's lock-free fast path is not, about 2.2× the
  baseline at eight CPUs. A weighted admission pays that for counting cost
  rather than calls.
- **`Acquire_Contended_FIFO`** — every P asking 3 of 4 units, so nearly every
  `Acquire` waits behind another and is granted by a `Release`: the waiter's
  path, its node and channel allocated (1 alloc/op amortised, 143 B) and a
  park and a wake per acquisition.
