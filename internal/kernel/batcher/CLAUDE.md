# internal/kernel/batcher/

## Purpose

Generic coalescing buffer (ADR 0014): `Batcher[T]` accumulates items via `Add`
and hands them to a `Sink[T]` closure in batches — flushed eagerly at a
`MaxItems` / `MaxWeight` cap, on an optional background `FlushEvery` ticker, or
on an explicit `Flush` / `Close`. Domain-neutral by construction: the only axis
of difference between the s3 (byte-weighted) and cloudwatch (event-counted)
sinks is captured by `WeightOf`, and every other sink-specific concern
(chronological reorder, per-batch key) rides inside the caller's deliver
closure. Code range `0x00_01_05_*` (0.1.5).

## Surface

```go
type Sink[T any] func(ctx context.Context, batch []T) error

type Config[T any] struct {
    MaxItems   int                 // eager-flush at this many items (<=0 disables)
    MaxWeight  int64               // eager-flush at this summed weight (needs WeightOf)
    WeightOf   func(T) int64       // nil => count-only (each item weighs 1, MaxWeight ignored)
    FlushEvery time.Duration       // >0 spawns a ticker joined by Close
    OnError    func(error)         // observes background ticker deliver failures (nil => no-op)
}

func NewBatcher[T any](deliver Sink[T], cfg Config[T]) *Batcher[T]
func (b *Batcher[T]) Add(ctx context.Context, item T) error  // eager flush on cap; Closed after Close
func (b *Batcher[T]) Flush(ctx context.Context) error        // synchronous; propagates deliver error
func (b *Batcher[T]) Close(ctx context.Context) error        // stop ticker + final flush; idempotent

// Sentinels (errs.Define-backed, kernel-range codes).
var Closed        *errs.Error // 0.1.5.1 BATCHER_CLOSED
var DeliverFailed *errs.Error // 0.1.5.2 BATCHER_DELIVER_FAILED
```

## Conventions

- **Owns its own ticker.** `Batcher` drives a plain `time.Ticker` directly — it
  does NOT depend on any other kernel lifecycle primitive (ADR 0014 §D6:
  coupling two new primitives in one commit is needless risk).
- **Swap-under-lock, deliver-outside-lock.** The pending batch is swapped out
  under `mu` and the `Sink` runs outside it, so a slow delivery never blocks a
  producer. `Add` / `Flush` / `Close` are all safe under concurrent callers.
- **Serial Sink invocation (V6).** The `Sink` is invoked under a dedicated
  `deliverMu` held only across the call, separate from `mu`, so two flush paths
  (a cap-triggered `Add` racing the ticker, or two cap `Add`s) never enter the
  closure concurrently. A `Sink` may assume serial invocation; cross-batch
  ordering is still not guaranteed. The separate mutex keeps a slow `Sink` from
  blocking producers appending into the next batch.
- **Closure carries the domain bits.** A pre-delivery reorder (CloudWatch's
  chronological `PutLogEvents` requirement) or a per-batch key (S3's object key)
  lives in the deliver closure — never in the batcher.
- **`WeightOf` is the byte-vs-count axis.** Nil = count-only (each item weighs 1,
  `MaxWeight` ignored). Set it to a byte-size function for byte-capped batching.
- **Two codes only.** `Closed` (Add/Flush after Close) and `DeliverFailed`
  (the closure errored, wrapped so `errors.Is` reaches the cause).

## Sentinels

| Var | Code | Reason | Returned by |
|---|---|---|---|
| `Closed` | `0x00_01_05_01` (0.1.5.1) | `BATCHER_CLOSED` | `Add` / `Flush` after `Close` |
| `DeliverFailed` | `0x00_01_05_02` (0.1.5.2) | `BATCHER_DELIVER_FAILED` | `Add` / `Flush` / `Close` when the `Sink` errors |

Match with `errs.HasCode(err, batcher.CodeBatcherDeliverFailed)` or
`errors.Is(err, batcher.DeliverFailed)`.

## Do NOT

- Reach into `core/*` or `service/*` — kernel is stdlib + sibling-kernel only
  (`batcher` imports just `context`, `sync`, `time`, and `internal/kernel/errs`).
- Move a sink-specific reorder / key / weight into the batcher — keep it in the
  deliver closure or `WeightOf`, or the dedup that justifies this package is lost.
- Add a blocking / unbounded variant without an ADR.

## Verification

```
bazel test --config=race //internal/kernel/batcher:batcher_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./batcher
```

Tests: `batcher_external_test.go` (public contract: coalescing Flush, eager
MaxItems / MaxWeight flush, wrapped deliver error, Close + final flush +
Closed-after-Close + idempotent second Close, background ticker),
`batcher_internal_test.go` (`weigh` count-only fallback, concurrent
producers + flusher race, deliver-closure-controls-order proof).
