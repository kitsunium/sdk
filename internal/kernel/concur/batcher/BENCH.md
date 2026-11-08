<!-- generated from internal/kernel/concur/batcher/batcher_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./concur/batcher/` to refresh -->
# Benchmarks — `internal/kernel/concur/batcher`

Generic coalescing buffer (ADR 0014). These benchmarks isolate the producer-visible
`Add` path under a small `MaxItems` cap so nearly every `Add` triggers an eager
flush through the deliver path, plus the no-op `Flush` fast path as a
serialization-free baseline. They exist to quantify the cost of the **V6**
Sink-serialization fix: the deliver closure now runs under a dedicated
`deliverMu` so two flush paths never enter the Sink concurrently.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 12 (12th Gen Intel(R) Core(TM) i7-1255U) |
| RAM                | 15.3 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 (Debian 13 trixie) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | fix/bench-127 |
| Git commit         | c08d730 |
| Generated (UTC)    | 2026-09-15 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median of 5 runs |

> **What these numbers support.** `allocs/op` is exact: all 5 repeats agreed on
> every cell, in every package. `B/op` is exact too **except where a cell
> carries `*`**, which marks five values that were not identical and a median
> reported in their place. Both columns are **unchanged** from a go1.26.4 run
> of this same code on this same box (124 benchmarks compared SDK-wide, 44 of
> them allocating, zero counter moved). `ns/op` are medians and carry the
> `spread` shown, which is a **within-run** figure that understates run-to-run
> variance: re-running the identical binary on this box moved individual cells
> by up to 94 %. Read ns/op as an order of magnitude on this box, never as a
> cross-edition or cross-machine delta.

## V6 before/after — Sink serialization cost

The plan (PR-F · WS-7, V6) requires a before/after benchmark because serializing
the Sink behind a dedicated mutex can cost throughput. "Before" is the prior code
path (deliver invoked with no `deliverMu`); "after" is the serialized path now in
`deliverBatch`.

```
                       before (unserialized)        after (deliverMu, V6)
BenchmarkAdd_CapFlush   27.71 ns/op  15 B/op  0 a   25.63 ns/op  15 B/op  0 a
BenchmarkAdd_Contended 105.7  ns/op  17 B/op  0 a  108.1  ns/op  16 B/op  0 a
```

The single-producer cap-flush path is unchanged (the mutex is uncontended — a
lock/unlock pair amortised across 16 Adds). The contended path moves from
~105.7 to ~108.1 ns/op — within run-to-run noise on this box, NOT a material
regression. Per the V6 acceptance fallback, no regression means **serialize is
kept as the default** and **no `ConcurrentSink` opt-in is introduced** — an
implicit concurrent contract was the footgun the finding flagged.

## Results (current, serialized)

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/concur/batcher
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark          median ns/op   spread   min–max         B/op   allocs/op
Add_CapFlush-12           52.13     9.2%   50.73 – 55.51     15           0
Add_Contended-12          320.1     2.2%   314.5 – 321.7     17           0
Flush_Empty-12            41.70     5.4%   41.32 – 43.57      0           0
```

## How to read this

- **`BenchmarkAdd_CapFlush`** — single producer, `MaxItems: 16`. Every 16th Add
  flushes through `deliverBatch` and acquires `deliverMu` uncontended. The ~15 B/op
  is the amortised growth of the pending slice's backing array, not a per-Add
  allocation (it benches at 0 allocs/op).
- **`BenchmarkAdd_Contended`** — the same path under `RunParallel`, so `deliverMu`
  is contended across all P goroutines. This is the worst case for the V6 fix and
  the number the before/after comparison is read against.
- **`BenchmarkFlush_Empty`** — an empty `Flush` short-circuits under `mu` before
  reaching the deliver mutex, so it is the serialization-free floor (~42 ns/op, 0
  B/op) the cap-flush benches are measured against.

## Edition 2026-11-03 — lifetime context, bounded `Close`, `FlushOn` (#1040, #817)

One box, before and after interleaved (the table above is another box): AMD
EPYC 7351P (8 vCPU), Linux 6.12, go1.27.3, `GOMAXPROCS=4`, `-benchmem -count
8`, medians. Command:
`cd internal && go test -run '^$' -bench . -benchmem -count 8 ./kernel/concur/batcher/`.

| Row | Before (base `42143bf47`) | After | |
|---|---|---|---|
| `Add_CapFlush` | 38.0 ns, 15 B, 0 allocs | 38.1 ns, 15 B, 0 allocs | unchanged (#1040: within 3 %) |
| `Add_Contended` | 106.6 ns (97.9–118.0), 15 B | 109.1 ns (107.9–115.7), 16 B | +2.3 %, within the before row's spread; a `RunParallel` row on a shared box |
| `Flush_Empty` | 40.6 ns | 24.9 ns | −39 %: the closed check and the swap share one lock section |

The delivery stays serialized by a mutex. A one-token channel semaphore,
which any wait could leave, measured `Add_CapFlush` at 42.2 ns (+11 %), so the
two waits that must be able to give up — the batcher's goroutine and `Close` —
take the mutex by `TryLock` and wait on a wake-up a release sends only while
one of them is counted.

### `Add_CapFlush_SlowSink` — a 1 ms Sink at `MaxItems` 16 (#817)

Bursts of 32 `Add`s (two batches), the clock stopped while a `Flush` waits
the Sink out between bursts: `-bench=SlowSink -benchtime=3200x -count 6`.

| `FlushOn` | Producer ns per `Add` |
|---|---|
| `FlushCaller` (the zero value, the previous behaviour) | 67 766 (67 647–71 802) |
| `FlushBackground` | 353 (282–481), 22 B, 0 allocs |

Under `FlushCaller` every 16th `Add` runs the 1 ms Sink: ≥ 62 µs per `Add`
averaged, as #817 computed. Under `FlushBackground` the producer appends, hands
the batch to the batcher's goroutine and returns — #817's "< 1 µs" is met. A
producer that outruns the Sink by two batches waits for room, so sustained
throughput is still the Sink's: background delivery moves the wait off the
producer's path for a burst, it does not make the Sink faster.
