# dbsink — driver-agnostic database sink shell

> Internal package (`internal/service/writer/dbsink`). Not a public API: it is
> consumed only by ADR-gated `third-party/db/writer/*` driver adapters, never by
> SDK end users. Consumer-facing prose for the DB writers lives with those
> adapters once ADR 0015 lands.

`dbsink` is the database analogue of the S3 batching sink: it coalesces log
records into batches via the generic kernel batcher and delivers each batch
through one seam — `execBatch func(ctx, []RecordEvent) error` — when the row cap
is reached, on an optional ticker, or on `Flush` / `Close`. It imports no
database driver and no vendor SDK, so it stays stdlib-only and dep-light; a
concrete driver supplies only its `execBatch` closure and a plain-data `Config`.

## Composition

`Compose(exec, cfg)` returns `levelgate(async(dbSink))` behind the
`core/logger.Sink` interface:

- **levelgate** — drops records below `cfg.MinLevel` before they reach the ring.
- **async** — non-blocking ring buffer with `OnDrop` back-pressure.
- **dbSink** — batches the surviving records and calls `execBatch` once per
  batch.

This is the same ordering the s3 writer factory wires.

## Allocation note

`Write` benches at 0 allocs/op *amortised*, but this sink makes **no zero-alloc
contract**: it defers delivery on the async drainer goroutine and carries an
amortised batch-buffering cost. The SDK's hard zero-alloc guarantee is the
producer's `Build().Send()` path (ADR 0014), not a batching sink. See
[`BENCH.md`](./BENCH.md) and [`CLAUDE.md`](./CLAUDE.md).

## Status

ADR 0015 is gated (merge blocked on ADR approval / WI-8). The shell compiles and
tests green today; it becomes reachable once a concrete `third-party/db/writer/*`
adapter wraps `Compose` and registers a `core/writer` factory.
