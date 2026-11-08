# ADR 0216 — a pool bounds workers, a semaphore bounds weight, and the ring takes many producers

- **Status**: Accepted
- **Date**: 2026-11-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0006](0006-sdk-error-code-registry-extension.md) (the ring's layout and its MPSC sibling), [ADR 0010](0010-kernel-recycler-primitive.md) (`buffer`'s size classes), [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md) (`batcher`'s lifetime context and background delivery), [ADR 0049](0049-cache-becomes-a-domain.md) (`singleflight`'s shared context keeps its root in the call record), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) §2 and §4 (the ring's multi-producer mode delivered; `pool` published). Two new kernel packages, `internal/kernel/concur/pool` (`0.1.6.*`) and `internal/kernel/concur/sema` (`0.1.7.*`).
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a refusal, never a park), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published interface grows by a sibling), [ADR 0072](0072-health-bounds-every-wait-it-owns.md) (every wait bounded), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (the allocation budgets), [ADR 0207](0207-concurrent-callers-share-one-flush-and-each-gets-its-verdict.md) (`commit`, the primitive beside these), issues [#804](https://github.com/kitsunium/sdk/issues/804), [#805](https://github.com/kitsunium/sdk/issues/805), [#810](https://github.com/kitsunium/sdk/issues/810), [#812](https://github.com/kitsunium/sdk/issues/812), [#817](https://github.com/kitsunium/sdk/issues/817), [#1034](https://github.com/kitsunium/sdk/issues/1034), [#1040](https://github.com/kitsunium/sdk/issues/1040), [#813](https://github.com/kitsunium/sdk/issues/813)

## Context

The first pass of the continuous audit (#802) found the kernel's concurrency
family short of three shapes every engine above it rewrites, and four of its
existing primitives paying for choices nobody had measured:

- **No long-lived workers.** `group` is a one-shot fan-out (a goroutine per
  task, 2.1 µs and 5 allocations for one) and `worker` one daemon whose
  `CLAUDE.md` refuses pools. Health's readiness probe starts a goroutine and a
  context per check per probe, the scheduler a goroutine per fire, `forward`
  its own senders; a downstream service building a request pipeline writes it
  again, usually without the panic capture `group` already has.
- **Admission counts calls, never their cost.** `group`'s limit, the
  bulkhead and the waiting bulkhead are unit semaphores: a call that needs a
  64 KiB buffer is admitted like one that needs 1 KiB, and
  `golang.org/x/sync/semaphore` is unreachable below `third-party/` (ADR
  0156).
- **The only lock-free queue is single-producer**, so both multi-producer
  users wrap it in a mutex (the async logger, the network loop's post queue),
  and ADR 0159 §2's multi-producer mode was still "tomorrow".
- **The SPSC ring** laid both cursors on one cache line, loaded both on every
  operation and divided on every index (#1034).
- **`buffer`** handed every consumer a 1 KiB buffer from one pool that kept
  anything up to 64 KiB, so sizes mixed: a 16 KiB read buffer allocated per
  connection, a 64 KiB one returned by the server went to the logger next
  (#810).
- **`batcher`** ran a cap-tripped batch's Sink on the producer's goroutine
  (#817), its ticker under `context.Background`, and `Close` joined the ticker
  with a bare receive (#1040): a Sink stuck on the network held `Close` for as
  long as it was stuck, whatever deadline its caller gave.

## Decision

1. **`concur/pool`** — `NewPool(ctx, Config{Workers, Queue, OnPanic, OnError})`,
   `Submit(ctx, Task) error`, `Drain(ctx) error`, `Stop(ctx) error`. `Workers`
   below 1 is 1. `Queue` 0 is no queue: `Submit` hands the task to an idle
   worker or returns `PoolFull` (`0.1.6.1`) at once — a refusal, never a park
   (ADR 0031); a positive `Queue` is waited on within the submitter's context,
   `PoolFull` then wrapping its error. After `Drain` or `Stop`, `PoolClosed`
   (`0.1.6.2`). `Drain` stops accepting, runs what is queued and joins within
   its context; `Stop` abandons the queue, cancels the tasks' context and
   joins within its context — no join of the pool is unbounded (ADR 0072).
   A task's panic is recovered on its worker into a `PanicValue` (the value,
   the worker's stack) for `OnPanic`, and the worker serves the next task;
   with no `OnPanic` it is raised again, so a fault nobody observes is never
   swallowed. Published as `pkg/v1/concur/pool` (ADR 0159 §4): a downstream
   service is the consumer #804 names first.
2. **A task runs under the pool's context**, New's, cancelled by `Stop` — not
   a merge of the submitter's values with the pool's cancellation, which #804
   sketched. That merge is a context per task, an allocation per `Submit`;
   the contract is 0 allocations in steady state (`design/sdk.yaml` budget on
   `(*Pool).Submit`), so the values a task needs travel in its closure, which
   is the caller's.
3. **`concur/sema`** — `Weighted` from `NewWeighted(capacity int64)` (below 1 is 1):
   `Acquire(ctx, n)` FIFO — a waiting request is served before every later
   one, so a large one is never starved —, cancellable, `WeightTooLarge`
   (`0.1.7.1`) at once for `n` above the capacity; `TryAcquire(n) bool`;
   `Release(n)`, which panics on more than is held (recycler's position on a
   programmer's fault). A mutex, a counter and an intrusive waiter list, each
   waiter with a channel; nothing allocated uncontended. Kernel-only, like
   `commit` (ADR 0207), until a domain adopts it — its adopters (a weighted
   bulkhead, `forward`'s byte budget, the server's read buffers in flight) are
   siblings of their own domains (ADR 0039).
4. **`ring.NewMulti[T](capacity) (MultiQueue[T], error)`** — the MPSC
   sibling: `MultiQueue` has `Queue`'s four methods under the wider contract
   (any number of `TryWrite` callers, exactly one `TryRead` caller), so it is
   also a `Queue` and `Drain` reads it; `Queue` stays frozen (ADR 0039). It is
   Dmitry Vyukov's bounded queue with its many-consumer half reduced to one: a
   writer claims a position with a compare-and-swap on the shared cursor,
   stores into the cell, and publishes it through the cell's sequence number;
   the capacity is rounded up to a power of two, which `Capacity` reports. A
   writer between its claim and its store makes the reader answer `Empty` for
   that cell and the ones behind it. The sentinels are the ring's three.
5. **The SPSC ring keeps its API and changes its layout** (#1034): the
   read-only header, the consumer's cursor with its cached copy of the
   producer's, and the producer's cursor with its cached copy of the
   consumer's, each a cache line (64 bytes) from the next; a far cursor is
   loaded only when the ring looks full or empty; the cursors count every item
   and a slot is the cursor masked into a power-of-two storage. `Capacity` and
   `Len` answer as before.
6. **`buffer` has size classes** (#810): 1, 4, 16, 64 and 256 KiB, one
   `recycler.CappedPool` each, whose factory makes exactly the class's size and
   which keeps a returned buffer only while its capacity is at most twice the
   class. `GetSize(n)` serves the smallest class that fits (above 256 KiB,
   allocated to measure); `Put` routes a buffer by its capacity to the largest
   class it covers; `Get()` is the 1 KiB class. `net/server` takes its read
   buffer with `GetSize(ReadBufferSize)`.
7. **`batcher` gets a lifetime context and a delivery mode** (#1040, #817).
   Every delivery the batcher's own goroutine makes runs under a context
   `Close` cancels. `Close(ctx)` joins that goroutine within `ctx` while it is
   inside a delivery — when `ctx` ends first it cancels the delivery's context
   and returns `BatcherCloseTimedOut` (`0.1.5.3`, the field `undelivered`
   counting what it left) — and waits for the Sink, held by a caller's flush,
   within `ctx` too. `Config.FlushOn`: `FlushCaller`, the zero value, today's
   behaviour; `FlushBackground` hands a capped batch to the batcher's
   goroutine, one batch waiting beside the one being delivered, an `Add` that
   fills a second waiting for room within its context; the failure goes to
   `OnError`.
8. **`singleflight`'s shared context keeps its root in the call record**
   (#813, the kernel's row of its ten). The root
   `context.WithoutCancel(leader)` built — the leader's values, no deadline,
   no cancellation, the same name — is a field of the call, and
   `context.WithCancel` is built over it: the shared context is the same,
   one allocation cheaper. #813 sketched one context type cancelling itself;
   it is not taken, because the context package treats a cancelling context
   it does not know differently — `context.Cause` would reach the leader's
   own cause, and every child would be wrapped or watched by a goroutine.

## Consequences / Semantics

Measured on the 8-vCPU EPYC 7351P lane, `GOMAXPROCS=4` unless named, the
packages' `BENCH.md` holding the tables:

- **ring SPSC**: the producer→consumer hand-off at depth 1024 went from
  289–458 to 109–127 ns per record, at 65 536 from 286–411 to 31–95 ns; `Len`
  4.9 → 2.9 ns. `TryWrite_Happy` / `TryRead_Happy` (a write and a read per
  iteration) stay at ~20 ns: two sequentially consistent stores bound them,
  and Go has no release-only store. #1034's "≤ 50 % of the committed row" for
  those two rows is therefore not met on this lane; its hand-off criterion
  (≤ 60 %) is.
- **ring MPSC**: 0 allocations on both paths (budgets). One producer hands a
  record over in ~25 ns where the mutex-wrapped SPSC ring takes ~85; at four
  producers saturating the ring, ~410 ns per record against ~235 for the
  mutex, and at eight on eight CPUs ~714 against ~384 — the writers' claims
  contend on one cursor while Go's mutex parks the losers. #805's "four
  producers ≤ 2× SPSC" is not met, and so **the async logger keeps its
  mutex**: adopting the sibling there would also have made `DropOldest` a
  consumer-side policy (a producer can no longer read), a change of what the
  policy guarantees, for a throughput the measurement does not show. The
  network loop's post queue (L2's) is left as it is.
- **pool**: `Submit` in steady state allocates nothing (budget).
- **sema**: an uncontended `Acquire` + `Release` allocates nothing (budget).
- **buffer**: a 16 KiB `GetSize` round trip allocates nothing (budget);
  `net/server`'s acquire and release at `ReadBufferSize` 16 KiB went from
  6.4–7.0 µs, 17 487 B and 4 allocations to ~160 ns, 0 B and 0 allocations. A
  buffer grown past twice its class is dropped: a logger record between 8 and
  16 KiB, which the shared pool kept, is now reallocated — the price of never
  handing a 1 KiB caller a 64 KiB buffer.
- **batcher**: under `FlushBackground` and a 1 ms Sink at `MaxItems` 16 the
  producer pays ~350 ns per `Add` against ~68 µs under `FlushCaller`;
  `Add_CapFlush` and `Add_Contended` are unchanged within noise, `Flush_Empty`
  40 → 25 ns (one lock section instead of two). `Close` on a context that has
  already ended still joins an idle goroutine and still attempts the final
  batch under that context, as before.
- **singleflight**: a leading `Do` allocates 5 objects where it allocated 6,
  in every row that leads (benchstat, n = 15, p < 0.001); B/op and time
  unchanged; budget `allocs: 5` on `(*Group[...]).Do`. What fn's context
  shows — values, deadline, `Done`, `Err`, `Cause`, name, children,
  `AfterFunc` — and what `Do` returns are those of the construction before,
  held by a test written against it and passing on both. fn's context now
  keeps the call record reachable, so an fn that hands it to work outliving
  the call keeps the result alive with it.

## Breaking changes

None to an existing signature. New symbols: `ring.MultiQueue`,
`ring.NewMulti` (and their `pkg/v1/collections/ring` aliases);
`batcher.FlushMode`, `FlushCaller`, `FlushBackground`, `Config.FlushOn`,
`BatcherCloseTimedOut` (and aliases); `buffer.GetSize`; the packages `pool`
(and `pkg/v1/concur/pool`) and `sema`. Visible changes: a `batcher.Close`
whose context ends while the ticker is inside a Sink now returns
`BatcherCloseTimedOut` instead of waiting for the Sink; `buffer.Put` drops a
buffer grown past twice its class, which the shared pool kept up to 64 KiB.

## Alternatives considered

- **A merged per-task context in `pool`** (the submitter's values, the pool's
  cancellation): one allocation per task, refused for the 0-allocation
  `Submit` — see Decision 2.
- **`Stop()` without a context**, as #804 sketched: an unbounded join of a
  task that ignores its context, the shape #1040 just removed from `batcher`.
- **Padding each MPSC cell to a cache line**, and a writer's full check on
  the reader's cursor before the cell: both measured slower at four and eight
  producers than the plain cells (`BENCH.md`), and dropped.
- **`batcher`'s delivery serialization on a channel** (a one-token semaphore
  any wait could leave): `Add_CapFlush` +11 %. The mutex stays, and the two
  waits that must be able to give up — the batcher's goroutine and `Close` —
  wait on a wake-up a release sends only while one of them is counted.

## Deferred

- The adopters, each in its own lot and domain: health's readiness probe and
  the scheduler's fires on `pool`; a weighted bulkhead, `forward`'s byte
  budget and the server's read buffers in flight on `sema`; codec scratch,
  view render and the tlv encoder on `buffer`'s classes.
- The async logger on `MultiQueue`, until a measurement on the writers'
  claim — or a different MPSC shape — shows the saturated multi-producer case
  winning; the loop's post queue with it.
- #813's ten allocation rows: measured and attributed in
  `docs/alloc-hot-paths.md`; the kernel's own row is fixed here (Decision
  8), the other nine belong to the lots that own the packages.

## References

- Impl: `internal/kernel/concur/{pool,sema,buffer,batcher,singleflight}/`,
  `internal/kernel/collections/ring/`, `pkg/v1/concur/pool/`; design:
  `design/kernel/concur.yaml`, `design/kernel/collections.yaml`, budgets in
  `design/sdk.yaml`.
- Measurements: each package's `BENCH.md`.
