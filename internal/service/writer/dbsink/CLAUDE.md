# internal/service/writer/dbsink/

## Purpose

The **driver-agnostic database sink shell** (ADR 0015, gated). It is the DB
analogue of the S3 batching sink (`third-party/aws/writer/s3`): it coalesces
records into batches via the generic kernel batcher and hands each batch to a
single deliver seam — `execBatch` — when the row cap is reached, on the optional
flush ticker, or on `Flush` / `Close`. It imports **no database driver, no
vendor SDK, and no new core sibling**, so it stays stdlib-only and dep-light.

The concrete driver adapters (`third-party/db/writer/{mysql,clickhouse,redis}`,
ADR-gated) wrap `Compose` with their own `execBatch` closure and self-register
as `core/writer` factories. This shell owns the batching / back-pressure /
level-floor composition once, so every DB writer inherits identical wiring.

## Contents

| File | Role |
|---|---|
| `dbsink.go` | unexported `dbSink` (batching terminal sink), the `execBatch` seam type, `Write`/`Flush`/`Close`, the batcher `deliver` relay |
| `dbsink_config.go` | exported `Config` value type + `Compose` (the only exported constructor — builds `levelgate(async(dbSink))`) |
| `dbsink_bench_test.go` | `BenchmarkWrite_*` + table-driven functional tests (delivery, eager flush on `MaxRows`, `Close` final-batch drain, `MinLevel` gate, faithful record delivery, `OnError` routing) |
| `BENCH.md` | Write-path benchmark + the honest zero-alloc caveat |

No `codes.go` / `errors.go`: this shell defines **no** error codes. A deliver
failure is wrapped by the kernel batcher as its own `BATCHER_DELIVER_FAILED`
sentinel (the driver's cause stays reachable via `errors.Is`, origin wins). The
concrete drivers own their `0x20`/`0x21`/`0x22` octets for client-init / insert
failures — not this package.

## Why this shape

- **Seam, not interface.** `execBatch func(ctx, []RecordEvent) error` is a func
  type so the real driver call is an anonymous closure inside each adapter
  (confining the driver), and tests inject a recording closure — exactly the s3
  `uploadFunc` pattern.
- **Records, not bytes.** The seam carries `[]RecordEvent` (not `[]byte`): a DB
  sink persists the **structured** record and re-serialises per its wire
  protocol, so the encoded payload `p` handed to `Write` is ignored.
- **Compose owns the order.** `levelgate(async(dbSink))` — gate drops below-floor
  records before the ring; async gives a non-blocking ring + `OnDrop`
  back-pressure; dbSink batches the rest. Identical ordering to the s3 factory.
- **Count-based batching.** A DB insert is bounded by statement / pipeline row
  count, so the batcher uses `MaxItems` (not the byte-weight `MaxWeight` the s3
  object sink uses).

## Allocation posture (do NOT claim zero-alloc on Write)

`dbSink.Write` runs on the async **drainer** goroutine (the producer call already
returned at `async.Write`), and `RecordEvent` is immutable by contract, so **no
defensive per-record clone is made** — the batched value safely outlives async's
recycled entry. The only Write-side cost is the amortised growth of the batcher's
pending slice. `allocs/op` benches at 0 *amortised*, but a single grow-triggering
Write allocates: this is a deferring sink, and the SDK's hard zero-alloc contract
is the producer's `Build().Send()` path (ADR 0014), never a batching sink. See
`BENCH.md`.

## ADR-gate status

ADR 0015 is **gated** (merge blocked on WI-8 / ADR approval). This shell is
stdlib-only and adds no dependency, so it compiles and tests green today; it
becomes *reachable* only once a concrete `third-party/db/writer/*` adapter wraps
`Compose` and registers a factory — which lands behind the same gate.

## Do NOT

- Import a database driver / vendor SDK here — that belongs in the concrete
  `third-party/db/writer/*` adapter.
- Add a `core/writer` factory registration here — the shell is composed, not
  registered; the drivers register.
- Add a new `core` sibling or widen `RecordEvent` / the `Encoder` for DB
  metadata (frozen contracts).
- Claim a zero-alloc Write contract.

## Verification

```
# per-module (GOWORK=off honours the replace directives)
cd internal/service && GOWORK=off go test -race ./writer/dbsink/...

# Bazel (source of truth)
bazel test --config=race //internal/service/writer/dbsink:dbsink_test
bazel test --config=alloc //internal/service/writer/dbsink:dbsink_test

# dep-light: no driver must reach this package's import graph
go list -deps ./internal/service/writer/dbsink/... | grep -E 'mysql|clickhouse|redis' && echo LEAK || echo clean
```
