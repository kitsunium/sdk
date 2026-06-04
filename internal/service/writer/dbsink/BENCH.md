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
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.90+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.26.3 linux/amd64 |
| Git branch         | feat/dbsink (WI-9) |
| Git commit         | (current HEAD, pre-commit) |
| Generated (UTC)    | 2026-06-02 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkWrite_NoAttrs-12      	16449230	        66.46 ns/op	      15 B/op	       0 allocs/op
BenchmarkWrite_WithAttrs-12    	16468926	        72.29 ns/op	      16 B/op	       0 allocs/op
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
- **CPU/RAM minimality comes from coalescing, not from per-Write tricks.** ~80 ns
  per Write is the cost of one level check, one ring enqueue, and one slice
  append; the real saving is that N records become **one** `execBatch`
  round-trip, so the per-record database/CPU cost falls as the batch fills.
