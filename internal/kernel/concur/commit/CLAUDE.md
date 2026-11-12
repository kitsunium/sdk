<!-- updated: 2026-11-12T19:00:00Z -->
# internal/kernel/concur/commit/

## Purpose

Generic **group commit** (#803): `Group[T]` makes N concurrent callers that
each contribute an item share ONE flush, and hands that flush's verdict to all
N. A kernel primitive (stdlib-only AND generic — one type parameter, one
function, no domain word). Code range `0.1.4.*` (`COMMIT_CLOSED`). Decided by
**ADR 0207**.

It sits between two primitives the kernel already has: `singleflight`
deduplicates IDENTICAL calls (one key, one result); `batcher` coalesces items
ASYNCHRONOUSLY (`Add` returns before delivery, and the caller never learns the
verdict of the ticker's flush). Here every caller blocks until the flush that
carried its item has run, and gets its error — what a durable write needs.

## Contents

| File | Surface |
|---|---|
| `commit.go` | `Group[T]` + `New` / `Do` / `Close`, and the join / lead / seal path |
| `batch.go` | `batch[T]` — one flush's items, the channels that order and publish it, the verdict |
| `config.go` | `Config` — `MaxItems` / `MaxWait` / `Clock` |
| `idle_external_test.go` | the idle path: on the caller's goroutine only for a ctx that cannot end and no `MaxWait`; inline and batched flushes never overlap; a panic leaves the group idle (#813) |
| `perf_fixtures_test.go` | race off: the `DoIdle` fixture of the `allocs: 0` budget kit's `gen_perf_test.go` holds |
| `gen_codes.go` | `CodeCommitClosed` (`0.1.4.1`) and `Closed`, written by kit gen from `design/kernel/concur.yaml` |
| `gen_perf_test.go` | race off, written by kit gen from `design/sdk.yaml`'s budgets: the idle `Do`'s `allocs: 0` (#813) |

## How a batch runs

- **The first caller since the last seal starts a batch** and a goroutine that
  leads it; every later caller appends its item under the Group's mutex.
- **The leader gathers** for `MaxWait` on `Config.Clock` (`clock.System` when
  nil — never `time.Sleep`), or until the batch holds `MaxItems`, which seals
  it at once so the next caller starts another. `MaxWait` 0 means no
  gathering window: the batch flushes as soon as the one before it has — the
  coalescing then comes from the flushes' own duration (ADR 0031: no inert
  zero).
- **Flushes run one at a time, in the order their batches started**: each batch
  holds the previous one's `done` and waits on it before sealing. A batch keeps
  taking items while the one before it flushes — that is where group commit's
  gain comes from.
- **The flush runs on the leading goroutine, not on a caller's**, with the
  first caller's context values and none of its cancellation
  (`context.WithoutCancel`). A caller whose `ctx` ends first returns
  `ctx.Err()`, and its item is flushed all the same — a departure, not a
  withdrawal: the flush may already be writing it (singleflight's position).
  A verdict already published is delivered even when `ctx` is done too.
- **A panic in the flush** is recovered on the leading goroutine and re-raised
  in every participant, so it is neither lost nor a process crash nobody
  waiting can see.
- **An idle `Do` flushes in place** (#813, ADR 0207 amended 2026-11-12): a
  caller whose `ctx` can never end (`Done() == nil`), on a group with no
  `MaxWait` and nothing gathering or flushing, runs the flush on its own
  goroutine over its item alone — no batch, no channel, no goroutine; zero
  allocations for an item of size zero (`allocs: 0`, fixture `DoIdle` in
  `perf_fixtures_test.go`). While it runs it is the group's last flush
  (`inline`, an `inlineState`: none, behind a batch, latest): the callers arriving meanwhile gather into a
  batch whose `after` is a channel made only then (`last`, `inlineDone`),
  and `Close` waits on it too. A cancellable `ctx` or a gathering window
  keeps the batched path.
- **`Close`** refuses every later `Do` with `Closed` and returns once the last
  batch started has been flushed — and an idle `Do`'s inline flush, which it
  waits for even when no batch exists.

## Adoption

Adopted, with no API change:

- `internal/service/data/vfs` `os_dirflush.go`: `WriteAtomic`'s directory
  fsync (`ops.syncDir`, step 5) through a per-directory `commit.Group`, so
  concurrent publishers into one directory share it (#803, ADR 0207 and ADR
  0056 amended 2026-11-02).

Not yet: each is a sibling port of its own domain (ADR 0039), to be wired
where the domain is changed:

- `internal/service/data/vfs`: a `WriteAtomicAll` sibling — N temporaries, N
  file fsyncs, N renames, ONE directory fsync.
- `internal/service/data/queue` `file.go`: `Publish`'s durable step over
  `WriteAtomic`, and a `PublishAll` sibling over `WriteAtomicAll`.
- `internal/service/data/docstore` `persist.go`: the overlay entries of one
  write (document + versions) through one batch.
- `internal/service/app/mail/spool`: a burst published through `PublishAll`.
- `internal/service/observe/logger/middleware/multi` (and failover / route):
  concurrent `Flush` callers sharing one sink fsync instead of identity dedup.

## Do NOT

- Run a SHARED flush on a caller's goroutine. The caller who arrived first is
  the one most likely to leave first, and its cancellation must not decide
  whether the others' items are written. The idle path is the one exception,
  and only because it has no others and no cancellation: do not widen it to a
  cancellable `ctx`, nor let a caller join a flush running inline.
- Drop a departed caller's item. It may already be in the flush; "not written"
  could not be promised.
- Add a domain word (`Sync`, `Journal`, `File`) to the API — the flush is the
  caller's.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=10 ./concur/commit/
bazel test --config=race //internal/kernel/concur/commit:commit_test
```
