<!-- updated: 2026-11-03T22:00:00Z -->
# internal/kernel/concur/batcher/

## Purpose

Generic coalescing buffer (ADR 0014): `Batcher[T]` accumulates items via `Add`
and hands them to a `Sink[T]` closure in batches — flushed eagerly at a
`MaxItems` / `MaxWeight` cap, on an optional background `FlushEvery` ticker, or
on an explicit `Flush` / `Close`. Domain-neutral by construction: the only axis
of difference between the s3 (byte-weighted) and cloudwatch (event-counted)
sinks is captured by `WeightOf`, and every other sink-specific concern
(chronological reorder, per-batch key) rides inside the caller's deliver
closure. Code range `0x00_01_05_*` (0.1.5).

**Published as `pkg/v1/concur/batcher`** (ADR 0159 §4): a pure alias, so every exported shape here is public API — a renamed field, a changed signature or a new method on an exported interface breaks a consumer at compile time, allowed only while the module is v0 and said out loud (ADR 0040).

## Surface

```go
type Sink[T any] func(ctx context.Context, batch []T) error

type Config[T any] struct {
    MaxItems   int                 // eager-flush at this many items (<=0 disables)
    MaxWeight  int64               // eager-flush at this summed weight (needs WeightOf)
    WeightOf   func(T) int64       // nil => count-only (each item weighs 1, MaxWeight ignored)
    FlushEvery time.Duration       // >0 spawns a ticker joined by Close
    Clock      clock.Waiter        // the ticker's clock; nil => clock.System (a ManualClock in tests)
    OnError    func(error)         // observes the batcher goroutine's deliver failures (nil => no-op)
    FlushOn    FlushMode           // FlushCaller (zero) | FlushBackground — who delivers a capped batch
}

type FlushMode uint8
const ( FlushCaller FlushMode = iota; FlushBackground )

func NewBatcher[T any](deliver Sink[T], cfg Config[T]) *Batcher[T]
func (b *Batcher[T]) Add(ctx context.Context, item T) error  // eager flush on cap; BatcherClosed after Close
func (b *Batcher[T]) Flush(ctx context.Context) error        // synchronous; propagates deliver error
func (b *Batcher[T]) Close(ctx context.Context) error        // stop the goroutine within ctx + final flush; idempotent

// Sentinels (errs.Define-backed, kernel-range codes).
var BatcherClosed        *errs.Error // 0.1.5.1 BATCHER_CLOSED
var BatcherDeliverFailed *errs.Error // 0.1.5.2 BATCHER_DELIVER_FAILED
var BatcherCloseTimedOut *errs.Error // 0.1.5.3 BATCHER_CLOSE_TIMED_OUT
```

## Conventions

- **Owns its own ticker.** `Batcher` drives a `clock.Ticker` built on
  `Config.Clock` — the wall clock when nil — and does NOT depend on any other
  kernel lifecycle primitive (ADR 0014 §D6: coupling two new primitives in one
  commit is needless risk). The clock is what lets a consumer's test flush by
  advancing a `ManualClock` past `FlushEvery` instead of sleeping through it.
- **Swap-under-lock, deliver-outside-lock.** The pending batch is swapped out
  under `mu` and the `Sink` runs outside it, so a slow delivery never blocks a
  producer. `Add` / `Flush` / `Close` are all safe under concurrent callers.
- **Serial Sink invocation (V6).** The `Sink` is invoked under a dedicated
  `deliverMu` held only across the call, separate from `mu`, so two flush paths
  (a cap-triggered `Add` racing the ticker, or two cap `Add`s) never enter the
  closure concurrently. A `Sink` may assume serial invocation; cross-batch
  ordering is still not guaranteed. The separate mutex keeps a slow `Sink` from
  blocking producers appending into the next batch.
- **A lifetime context for the batcher's own deliveries** (#1040, ADR 0216).
  `NewBatcher` derives `life` from `context.Background()`; every delivery the
  batcher's goroutine makes — a tick's, a background one's — runs under it,
  and `Close` cancels it on every exit. `Close(ctx)` joins the goroutine
  within `ctx` while it is inside a delivery (`busy`): when `ctx` ends first it
  cancels `life`, returns `BatcherCloseTimedOut` wrapping `ctx`'s error with
  the field `undelivered`, and leaves the final batch undelivered. An idle
  goroutine is joined whatever `ctx` is: once `life` is cancelled, a delivery
  it begins gives up before the Sink. The goroutine also waits for the Sink
  within `life`, and `Close` within `ctx`: `acquire` takes `deliverMu` by
  `TryLock`, then waits on `freed`, which `unlockDeliver` signals only while
  such a bounded wait is counted — so a caller's `Add` / `Flush` keeps the
  plain mutex path (`Add_CapFlush` unchanged; a channel semaphore measured
  +11 %). A Sink that ignores its context cannot be stopped from outside.
- **`FlushOn`** (#817, ADR 0216). `FlushCaller`, the zero value, delivers a
  capped batch on the `Add` that filled it (the previous behaviour, its error
  to that caller). `FlushBackground` makes the capped batch the `ready` one and
  kicks the batcher's goroutine (started by `NewBatcher` even without a
  `FlushEvery`); an `Add` that fills a second while `ready` is still waiting
  waits for `room` within its context — memory bounded at the batch being
  delivered, the ready one and the pending one —, its item staying pending
  when its context ends. A background failure goes to `OnError`. `Flush` and
  `Close` deliver `ready` before the pending batch, one `deliverMu` hold for
  both.
- **Closure carries the domain bits.** A pre-delivery reorder (CloudWatch's
  chronological `PutLogEvents` requirement) or a per-batch key (S3's object key)
  lives in the deliver closure — never in the batcher.
- **`WeightOf` is the byte-vs-count axis.** Nil = count-only (each item weighs 1,
  `MaxWeight` ignored). Set it to a byte-size function for byte-capped batching.
- **Three codes.** `BatcherClosed` (Add/Flush after Close), `BatcherDeliverFailed`
  (the closure errored, wrapped so `errors.Is` reaches the cause) and
  `BatcherCloseTimedOut` (Close's context ended first).

## Sentinels

| Var | Code | Reason | Returned by |
|---|---|---|---|
| `BatcherClosed` | `0x00_01_05_01` (0.1.5.1) | `BATCHER_CLOSED` | `Add` / `Flush` after `Close` |
| `BatcherDeliverFailed` | `0x00_01_05_02` (0.1.5.2) | `BATCHER_DELIVER_FAILED` | `Add` / `Flush` / `Close` when the `Sink` errors |
| `BatcherCloseTimedOut` | `0x00_01_05_03` (0.1.5.3) | `BATCHER_CLOSE_TIMED_OUT` | `Close` whose context ended before the goroutine stopped or the Sink was free |

Match with `errs.HasCode(err, batcher.CodeBatcherDeliverFailed)` or
`errors.Is(err, batcher.BatcherDeliverFailed)`.

## Do NOT

- Reach into `core/*` or `service/*` — kernel is stdlib + sibling-kernel only
  (`batcher` imports just `context`, `sync`, `time`, and `internal/kernel/{clock,errs}`).
- Move a sink-specific reorder / key / weight into the batcher — keep it in the
  deliver closure or `WeightOf`, or the dedup that justifies this package is lost.
- Add a blocking / unbounded variant without an ADR.

## Verification

```
bazel test --config=race //internal/kernel/concur/batcher:batcher_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./concur/batcher
```

Tests: `batcher_external_test.go` (public contract: coalescing Flush, eager
MaxItems / MaxWeight flush, wrapped deliver error, Close + final flush +
Closed-after-Close + idempotent second Close, background ticker),
`batcher_internal_test.go` (`weigh` count-only fallback, the ticker flushing
when an injected `ManualClock` passes `FlushEvery`, concurrent
producers + flusher race, serial `Sink` invocation (V6), every `Add` accepted
while `Close` runs is delivered, deliver-closure-controls-order proof),
`lifetime_external_test.go` (#1040: a `Close` bounded while a tick flush is
stuck in a Sink, the Sink cancelled and the ticker gone; the tick's context
ended by `Close`; the final batch bounded behind a caller's flush; #817:
`FlushBackground` never running the Sink on the producer, every item once
and in order, concurrent producers losing nothing, a background failure to
`OnError`), `batcher_bench_test.go` (cap-flush `Add`, uncontended and
contended, the empty `Flush` fast path, and a 1 ms Sink under each
`FlushMode` — the numbers are in `BENCH.md`).
