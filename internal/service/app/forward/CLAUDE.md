# internal/service/app/forward/

## Purpose

Store and forward for an export (ADR 0185): `Enqueue` keeps a batch in a
durable `data/queue` broker and returns once it is durable; `Run` sends each
batch through the caller's `Sender`, retried while its failure is retryable
and its age allows, dead-lettered when the sender refuses it for good, dropped
when it outlives `MaxAge`; `Stats` counts every step. `NewSpanExporter` and
`NewMetricExporter` put a trace or metrics batch in, encoded once as the
OTLP/JSON request the signal's OTLP/HTTP exporter posts — and that exporter's
`DocumentExporter.ExportDocument` is the `Sender` that posts it later. Public
facade: `pkg/v1/app/forward`.

Code range `0.3.93.*`, declared with its sentinels in
`internal/core/app/forward` (ADR 0160). This package declares no code.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `config.go` | `Config` (`Broker`, `Send`, `Retryable` required), `Sender`, `FullPolicy` (`DropOldest` the zero, `DropNewest`, `Block`) and `String`, the defaults (`DefaultMaxBatches`, `DefaultMaxBytes`, `DefaultBlockTimeout`, `DefaultSendTimeout`, `DefaultMaxAge`, `DefaultRetryBase`, `DefaultRetryMax`, `DefaultRecountInterval`), `validate` (every refusal named by field) and `normalized` |
| `forwarder.go` | `Forwarder`, `New` (the backlog counted through `MessageReader`), `Enqueue` (the bound, the full policy, then `publishCounted`: the publication and its identifier counted under `publish`), `Stats` (asks for a count once `RecountInterval` passed), `Run` (`Consume` with `AutoExtend` and `BatchSize` 1, beside the goroutine that counts the queue when asked), `dropOldest` (receive and acknowledge), `block` (a wait on the clock, woken by the one-slot signal a leaving batch sends), `hold` / `release` / `settle` / `leave` / `forget` (the counts by identifier; a batch not counted waits for a publication in flight, then is another process's and takes nothing) |
| `recount.go` | issue #580: `list` (every live message, its size and whether it is leased), `recountLoop` / `recountOnce` (a count per request, on Run's goroutine), `forgetGone`, `countListed`, `uncountGone` (the counts made what the listing holds, but for a batch kept during it, settled and still leased, or back unleased), `dueRecount` / `askRecount` (a one-slot request, never blocking) |
| `deliver.go` | the queue handler: an expired batch acknowledged unsent, the retry (`resilience.NewRetry`, `MaxInt` attempts, `Retryable` AND young enough, `Delay` the collector's `RetryAfter` or `Backoff.Delay`, held at what is left of the age), one attempt bounded by `SendTimeout` on the clock, the verdict: sent, handed back on a stop, `DoNotRetry` for a permanent refusal, expired |
| `signal.go` | `NewSpanExporter` / `NewMetricExporter`: the producer adapters, `EncodeOTLPJSON` then `Enqueue` |
| `stats.go` | `StatsValue` |
| `forward_external_test.go`, `signal_external_test.go`, `harness_external_test.go` | every behaviour on a `ManualClock` that reports each timer it arms, no sleep: refusals, sent and counted, a collector down then back on the backoff, a `Retry-After` waited in full and never past the age, a 400 dead-lettered once, an expired batch dropped unsent, each full policy, a stop that hands the batch back, the bound on one attempt, batches that survive a restart on the file broker; end to end through the real OTLP/HTTP exporter (429 then 400) |
| `recount_external_test.go` | issue #580, each case failing before it: a dead letter replayed while the head batch is retried is counted at the next count, and both sent leave nothing; a batch another consumer took is no longer counted; a batch another process queued is counted, and its delivery takes nothing from the forwarder's; the caller's `RecountInterval`; four producers, four consumers and a count at every read and settle leave no residue |
| `forward_bench_test.go` / `BENCH.md` | the cost of `Enqueue`, the producer's path, on the memory and the file broker |

## Why-this-shape

- **A batch is retried in the handler, not nacked with a delay.** A message
  nacked for a delay is invisible until it ends; while the collector is down
  every batch would be, `DropOldest` would find nothing to drop exactly when
  it matters, and every batch would try the dead collector once per round.
  Retrying the head in the handler keeps the backlog visible, the order kept,
  and the load on a down collector at one request per consumer. `AutoExtend`
  keeps the lease alive for as long as the retries take.
- **The age is the budget.** Attempts are unbounded (`math.MaxInt`); the
  classification decides what is retried and `MaxAge` — read from the
  broker's `EnqueuedAt`, which survives a restart — when it stops. A
  collector's `Retry-After` is waited in full, never cut to the backoff
  ceiling as `RetryConfig.MaxDelay` would, and never past the age. An expired
  batch is acknowledged and counted, not dead-lettered: the dead-letter store
  is unbounded, and a long outage would fill it with every batch it delayed.
- **The counts follow the queue (ADR 0185 amended, #580).** `Pending` and
  `PendingBytes` are the batches counted by identifier: started by `New`
  from `MessageReader`, added by `Enqueue` with the identifier the broker
  minted — under `publish`, held from the publication until it is counted,
  so a batch that leaves before it is counted waits for it rather than being
  taken for another process's —, taken back by the settle of a batch
  counted. Another process changes the queue behind it — `otlp replay`, a
  second instance consuming the same table, a broker dead-lettering on its
  own —: `Run` lists the queue again when a read of `Stats` or a settle finds
  `RecountInterval` passed, and at once when it delivers a batch it did not
  count — once per interval at most (`prompted`), or a second instance
  feeding the queue would have it listed once per batch. No timer: a manual
  clock moves only when the test moves it. The
  listing races the queue (`MessageReader`'s contract), so three cases are
  the forwarder's word: a batch kept during it stays counted; one settled
  stays uncounted while it is listed leased, because the consumer
  acknowledges after the settle (`leaving`, pruned by generation once a
  listing that began after the settle no longer holds it); one listed again
  unleased — replayed, or a lease lapsed — is counted again. The listing reads
  payloads, as `New` always did: its cost is the backlog's, on Run's
  goroutine, never `Enqueue`'s, which adds one map entry.
- **`Block` arms no timer while there is room**, and wakes on a one-slot
  signal channel every batch leaving sends to without blocking. One slot is
  enough because `Enqueue` is serialised — at most one producer waits — and a
  signal nobody waited for only makes the next wait look again; nothing is
  closed, so nothing can be closed twice.

## Do NOT

- **Nack a retryable failure.** See above; `DoNotRetry` is the one queue
  mark the handler returns.
- **Write anything anywhere.** The forwarder counts; a caller exports `Stats`
  where it wants (ADR 0149's division).
- **Decode a stored batch.** It is posted as it was written.
- **Retry inside the `Sender`** — the forwarder's retry is the only one.

## Verification

```
cd internal && go test -race -count=200 ./service/app/forward
cd internal && go test -run '^$' -bench . -benchmem ./service/app/forward
cd e2e && GOWORK=off go test -tags integration -run Forward ./integration/sql
```
