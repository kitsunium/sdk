<!-- generated from internal/service/writer/dbsink/dbsink_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./writer/dbsink/` to refresh -->
# Benchmarks — `internal/service/writer/dbsink`

Driver-agnostic database sink shell (ADR 0015, gated). The benchmark isolates
the **Write** path of the composed `levelgate(async(dbSink))` chain with a
no-op deliver seam under a drain-friendly config (`MaxRows: 64`,
`BufferSize: 1<<16`) so the batcher continuously flushes the seam and the ring
stays drained. It measures the producer-visible buffering cost — the clone-free
record append into the batcher behind the async ring — not a database round-trip.
Under sustained load the async ring can still saturate; a `BufferFull` is the
EXPECTED back-pressure signal (counted in `benchDrops`, not failed) — the
producer is never blocked on a slow DB.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | 12th Gen Intel(R) Core(TM) i7-1255U |
| CPU cores          | 12 |
| RAM                | 15.3 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 (Debian 13 trixie) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | fix/bench-127 |
| Git commit         | c08d730 |
| Generated (UTC)    | 2026-09-15 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median of 5 runs |

> **What these numbers support.** `B/op` and `allocs/op` are exact — all 5
> repeats agreed on every cell — and they are **unchanged** from a go1.26.4 run
> of this same code on this same box (124 benchmarks compared SDK-wide, 44 of
> them allocating, zero counter moved). `ns/op` are medians and carry the
> `spread` shown, which is a **within-run** figure that understates run-to-run
> variance: re-running the identical binary on this box moved individual cells
> by up to 94 %. Read ns/op as an order of magnitude on this box, never as a
> cross-edition or cross-machine delta.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/writer/dbsink
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark            median ns/op   spread   min–max         B/op   allocs/op
Write_NoAttrs-12            124.1    62.0%   111.5 – 188.5    *12           0
Write_WithAttrs-12          127.7    13.4%   112.2 – 129.3    *11           0

* marks a B/op cell whose value was not identical across the 5 repeats;
  the median is shown. allocs/op was unanimous on every row.
```

## How to read this — and why this sink makes NO zero-alloc *contract*

- **`allocs/op` reads 0, but Write is NOT a guaranteed zero-alloc path.** The
  count rounds to zero because (a) the async middleware borrows its per-Write
  carrier from a recycler pool, so no entry is allocated per call, and (b) the
  batcher appends the record into a pending slice whose backing-array growth
  *amortises* to ~0 allocs across a long steady-state run. A single Write that
  happens to trigger the batcher's slice growth **does** allocate — that is what
  the non-zero `B/op` (the amortised cost of those occasional grows) reflects.
- **The zero-alloc invariant belongs to the producer, not to this sink.** The
  SDK's "0 alloc on the hot path" guarantee is the `Build().Send()` path
  (ADR 0014). A *deferring* sink — one that batches records to hand them across
  an async boundary to a database — is downstream of that path on the drainer
  goroutine and is intentionally allowed an amortised buffering cost. This
  mirrors the s3 batching sink, which clones its byte payload per Write and makes
  no zero-alloc claim. dbsink avoids even the per-record clone (RecordEvent is
  immutable by contract, so the appended value is safe), so its Write is *as
  cheap as a batching sink can be* — but "cheap and usually zero" is not the same
  promise as the producer's hard zero-alloc contract.
- **CPU/RAM minimality comes from coalescing, not from per-Write tricks.** ~125 ns
  per Write is the cost of one level check, one ring enqueue, and one slice
  append; the real saving is that N records become **one** `execBatch`
  round-trip, so the per-record database/CPU cost falls as the batch fills.
