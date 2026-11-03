# ADR 0207 — concurrent callers share one flush, and each gets its verdict (`kernel/concur/commit`)

- **Status**: Accepted
- **Date**: 2026-11-02
- **Deciders**: SDK maintainers
- **Amends**: nothing published; a new kernel package, `internal/kernel/concur/commit`, code block `0.1.4.*`.
- **Related**: [ADR 0049](0049-cache-becomes-a-domain.md) (`singleflight`, the kernel primitive this one sits beside), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (no inert zero), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (adopters grow by siblings), [ADR 0054](0054-sdk-queue-domain.md) and [ADR 0056](0056-sdk-vfs-domain.md) (the durable writes that would adopt it), [issue 803](https://github.com/kitsunium/sdk/issues/803)

## Context

Every file-backed engine but consensus pays its device flushes per caller:
a `vfs` `WriteAtomic` is a file fsync and a directory fsync (3.06 ms against
0.195 ms unflushed, `vfs` BENCH), a durable queue `Publish` the same
(~300 messages a second per producer), a docstore `PutDisk` more. Consensus
got group commit in #558 — one sync per group of queued writes — and nothing
else did. The kernel had two neighbours and neither fits:

- `singleflight` deduplicates IDENTICAL calls: one key, one result. N callers
  writing N different items are N keys.
- `batcher` coalesces items ASYNCHRONOUSLY: `Add` returns before delivery, and
  the caller never learns the verdict of the ticker's flush. A durable write
  that cannot report its failure is not durable.

What was missing is the shape where N concurrent callers each contribute an
item, ONE of them runs the shared expensive operation, and EVERY participant
receives that operation's verdict.

## Decision

1. **`commit.Group[T]`, from `New(flush func(ctx, []T) error, Config)`.** `Do(ctx,
   item) error` adds the item to the batch being gathered — starting one if
   none is — and returns the verdict of the flush that carried it. `Close()`
   refuses every later `Do` with `Closed` (`COMMIT_CLOSED`, `0.1.4.1`) and
   returns once the last batch started has flushed. Stdlib-only and generic:
   no domain word (`Sync`, `Journal`, `File`) in the API — the flush is the
   caller's.
2. **`Config{MaxItems, MaxWait, Clock}`**, the zero usable: no item cap, no
   gathering window, `clock.System`. `MaxWait` 0 is not inert (ADR 0031): it
   means "flush as soon as the batch before has", and the coalescing then
   comes from the flushes' own duration — callers arriving during one flush
   ride the next. `MaxItems` seals a batch at once. The wait is on the
   injected clock, never `time.Sleep`.
3. **Flushes run one at a time, in the order their batches started**, and a
   batch keeps taking items while the one before it flushes — which is where
   group commit's gain comes from.
4. **The flush runs on a goroutine of its own**, with the first caller's
   context values and none of its cancellation. A caller whose `ctx` ends
   first returns `ctx.Err()` and its item is flushed all the same: a
   departure, not a withdrawal, because the flush may already be writing it
   (`singleflight`'s position). A verdict already published is delivered even
   when `ctx` is done too.
5. **A panicking flush** is recovered on its goroutine and re-raised in every
   participant, never lost and never a crash nobody waiting can see.

## Consequences

- Measured (`internal/kernel/concur/commit/BENCH.md`): at eight goroutines
  over a real fsync, one flush carries four to five callers' items
  (`fsyncs/op` ≈ 0.22). On the bench box's journal the wall time is not
  shared too — the device already merged concurrent fsyncs, the caveat #803
  named — so the gain is syscalls there, and wall time for callers that cannot
  issue their fsyncs concurrently (one producer writing a burst, a write path
  holding a lock across its flush) or on devices that do not merge.
- `Do_Uncontended` (~2 µs, 6 allocations) is the floor: a `Group` around work
  cheaper than that is a loss, around an fsync it is noise.
- The primitive lands with ONE adopter, which changes no API: `vfs`
  `WriteAtomic`'s directory fsync (step 5, ADR 0056) runs through a group per
  directory on the disk engine, so the publications into one directory that
  reach step 5 while a flush of it runs share the next one. Each still waits
  for a flush BEGUN after its own rename — it joins the batch still
  gathering, never the one flushing — and gets that flush's verdict
  (`os_dirflush_internal_test.go`, a mutation that joined the running flush
  fails it). Measured on the 4-CPU lane: 16 goroutines publishing into one
  directory 563 µs → 450 µs per publication (−20 %); one publisher unchanged
  in time, +8 allocations (the group's ~2 µs floor beside a 3 ms flush). Every
  queue file `Publish` and docstore `PutDisk` gets it without a change of
  their own.
- The other adopters are sibling ports of their own domains (ADR 0039), wired
  where that domain changes — listed in the package's `CLAUDE.md` §Adoption:
  a `vfs` `WriteAtomicAll` sibling (N temporaries, N file fsyncs, N renames,
  ONE directory fsync); the queue's file broker `PublishAll` over it;
  docstore's overlay entries of one write; a mail spool burst; and the logger
  `multi` / `failover` / `route` `Flush` sharing one sink fsync instead of
  deduplicating by identity.

## Alternatives considered

- **Extend `batcher` with a synchronous `Do`.** Rejected: its contract is that
  `Add` returns before delivery; a second, blocking verb with its own verdict
  path would give one type two incompatible meanings of "added".
- **Run the flush on the first caller's goroutine.** Rejected: the caller who
  arrived first is the one most likely to leave first, and its cancellation
  must not decide whether the others' items are written.
- **Let a departing caller withdraw its item.** Rejected: the flush may
  already be writing it, so "not written" could not be promised.

## References

- Impl: `internal/kernel/concur/commit/`; design: `design/kernel/concur.yaml`.
- Measurements: `internal/kernel/concur/commit/BENCH.md`.
- Consensus's own group commit: #558 (`internal/service/app/consensus`).
