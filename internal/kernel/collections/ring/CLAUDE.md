<!-- updated: 2026-11-07T20:30:00Z -->
# internal/kernel/collections/ring/

## Purpose

Lock-free single-producer / single-consumer (SPSC) bounded queue built on `sync/atomic`, and its multi-producer / single-consumer (MPSC) sibling (ADR 0216). Admitted to the kernel by ADR 0006 — domain-neutral by construction (any future metrics batcher, codec stream, event bus, or hot-path FIFO can plug in). Today the only consumer is `internal/service/observe/logger/middleware/async`; the surface is generic so other domains can adopt without a refactor. Code range `0x00_01_03_*` (`0.1.3.*`).

**Published as `pkg/v1/collections/ring`** (ADR 0159 §4): a pure alias, so every exported shape here is public API — a renamed field, a changed signature or a new method on an exported interface breaks a consumer at compile time, allowed only while the module is v0 and said out loud (ADR 0040).

## Surface

```go
type Queue[T any] interface {
    TryWrite(item T) (err error)        // non-blocking; returns ring.Full when saturated
    TryRead() (item T, err error)       // non-blocking; returns ring.Empty when no item
    Capacity() (n int)                  // logical capacity (slots usable)
    Len() (n int)                       // snapshot count (may shift mid-call)
}

// IFACE-PLUGIN: concrete struct (queueRing[T]) is unexported.
func New[T any](capacity int) (q Queue[T], err error)

// The MPSC sibling (#805, ADR 0216): Queue's four methods, any number of
// TryWrite callers, exactly one TryRead caller; capacity rounded up to a
// power of two. A MultiQueue is a Queue, so Drain reads it.
type MultiQueue[T any] interface { /* TryWrite, TryRead, Capacity, Len */ }
func NewMulti[T any](capacity int) (q MultiQueue[T], err error)

// The consumer's TryRead loop as an iterator: oldest first, until Empty or a
// break; the one consumer goroutine only; nothing allocated (issue #816). A
// FUNCTION and not a fifth method: Queue is a published interface (ADR 0039).
func Drain[T any](q Queue[T]) iter.Seq[T]

// Sentinels (errs.Define-backed, kernel-range codes).
var Full    *errs.Error // 0.1.3.1 RING_FULL
var Empty   *errs.Error // 0.1.3.2 RING_EMPTY
var CapZero *errs.Error // 0.1.3.3 RING_CAP_ZERO
```

## Conventions

- **SPSC contract is strict.** EXACTLY ONE goroutine calls `TryWrite` and EXACTLY ONE calls `TryRead`. Concurrent producers OR concurrent consumers are NOT safe — the caller serialises upstream (logger's async middleware takes a mutex) or wraps this primitive externally.
- **The cursors count, the storage is masked** (#1034). `head` and `tail` count every item ever read and written (they never wrap in practice: 2^64 operations); a slot is `cursor & mask` into a power-of-two storage at least `cap` long — never a division. Empty is `head == tail`, full is `tail - head == cap`, the logical capacity `New` was given, so `Capacity()` answers as before.
- **Each side's cursor on its own cache line** (#1034). The read-only header (`slots`, `mask`, `cap`), the consumer's group (`head`, `cachedTail`) and the producer's group (`tail`, `cachedHead`) are each a `linePad` (64 bytes, `cacheLine`) from the next, so a store by one side never invalidates the line the other reads; `Test_queueRing_CursorsOnTheirOwnLines` holds the gaps with `unsafe.Offsetof`. 64 under-pads Apple's 128-byte arm64 lines — known, not paid everywhere else.
- **The happy path is two sequentially consistent stores, and nothing else** (#1034). An iteration of `TryWrite_Happy` or `TryRead_Happy` (a write then a read) costs what `BenchmarkSCStorePair` — two `atomic.Uint64.Store` on padded words — costs on the same box: 21.1 and 20.7 ns against 20.8 on the EPYC box of `BENCH.md`'s 2026-11-11 edition. Every `sync/atomic` operation is sequentially consistent, and on amd64 such a store is an `XCHG`; a release-only store (a plain `MOV` there) would make the pair 5.7 ns, but Go offers none: `internal/runtime/atomic.StoreRel` is not importable, a `go:linkname` to it is refused by the linker and by the SDK, and a plain store is a data race the compiler may reorder. Do not weaken either store for a figure: the bar is the floor, `TryWrite_Happy` and `TryRead_Happy` within 10 % of `SCStorePair`.
- **The far cursor is cached.** The producer re-reads `head` only when the ring looks full against `cachedHead`, the consumer `tail` only when it looks empty against `cachedTail` — one atomic load per operation on the fast path. Each cache is written by its own side alone.
- **`Len` loads `head` before `tail`** and caps at `cap`: `tail` only grows, so a `tail` read after `head` is never behind it and the difference never underflows. The modulo arithmetic it replaced once reported an empty ring for the async logger's default ring holding 1009 records — a state in which its `Flush`, which waits for `Len() == 0`, could return with those records still queued. `Test_queueRing_LenAtEveryCursor` checks every cursor position and fill level of four small rings; `Test_queueRing_LenAfterWrap` pins those cases.
- **The MPSC sibling is Vyukov's bounded queue** (`multi.go`), its many-consumer half reduced to one: a writer claims a position with a CAS on `tail`, stores the item, then publishes the cell by its sequence number (`seq == pos+1`); the reader reads the cell whose `seq` says so and hands it one lap ahead (`seq = pos + len`). A writer between its claim and its store makes the reader answer `Empty` for that cell and those behind it (`Test_multiRing_ClaimedNotPublished`). The writers' `tail` and the reader's `head` are a cache line apart; the cells are not padded — padding them measured slower.
- **Measured, the MPSC claim does not beat a mutex under saturation.** One producer hands a record over in ~25 ns where the mutex-wrapped SPSC ring takes ~85; four and eight producers saturating the ring take ~410 and ~714 ns against ~235 and ~384 (`BENCH.md`). That is why the async logger keeps its mutex-wrapped SPSC ring (ADR 0216, #805) — and because its `DropOldest`, which evicts the oldest record and stores the new one in one critical section so the newest is always admitted, would have to move to the reader: a `MultiQueue` writer may not read.
- **Zero-alloc hot path.** `TryWrite` / `TryRead` return pre-allocated sentinel pointers (`Full`, `Empty`); no error is constructed at runtime. `errors.Is(err, ring.Full)` still works because the sentinels are stable package-level vars.
- **Slot cleared on read.** After dequeue the slot is zeroed (`b.slots[head] = zero`) so the GC can reclaim the item — no goroutine-private references survive past `TryRead`.
- **`IFACE-PLUGIN` marker** on `New` and `NewMulti` so the kernel can swap the backing implementation without breaking call-site code.
- **`allocateSlots[T]` helper.** Wraps the `make([]T, n)` so the slice-growth audit does not misclassify this fixed-length, indexed-only backing array.

## Sentinels

| Var | Code | Reason | Returned by |
|---|---|---|---|
| `Full` | `0x00_01_03_01` (0.1.3.1) | `RING_FULL` | `TryWrite` on saturated ring |
| `Empty` | `0x00_01_03_02` (0.1.3.2) | `RING_EMPTY` | `TryRead` on empty ring |
| `CapZero` | `0x00_01_03_03` (0.1.3.3) | `RING_CAP_ZERO` | `New` when `capacity <= 0` |

Match with `errs.HasCode(err, ring.CodeRingFull)` or `errors.Is(err, ring.Full)`.

## Do NOT

- Share a single `Queue[T]` across multiple producers or multiple consumers without external serialisation. The SPSC contract is a hard precondition, not a hint. Several producers take `NewMulti`; several consumers take nothing here.
- Spin in a tight loop on `TryRead` returning `Empty` without backoff — the consumer should wait on a wake signal from the producer or yield between attempts (the async middleware's drainer checks its stop channel, then yields with `runtime.Gosched`).
- Construct sentinels at the call site (`errs.Define(...)`) — reuse `ring.Full / Empty / CapZero` so identity-based comparisons stay zero-alloc.
- Add `Blocking` / `Bounded` variants here without an ADR — admitting a second concurrency contract to the kernel is a layer-policy decision, not a drive-by.

## Verification

```
bazel test --config=race //internal/kernel/collections/ring:ring_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./collections/ring
# coverage target: 90%
```

Tests: `multi_external_test.go` (`NewMulti`'s capacities and refusals, full / empty / order over three laps, and #805's race-on acceptance: eight writers, one reader, a million records, each read once in its writer's order), `multi_internal_test.go` (a claimed, unpublished cell; the cell cleared on read; the cursors' cache lines), `ring_external_test.go` (public contract: TryWrite/TryRead happy paths, `Drain` — every item oldest first, a break leaving the rest, full / empty sentinels, capacity reporting, Len snapshot, FIFO order, single-producer/single-consumer race coverage), `ring_internal_test.go` (cursor arithmetic, slot-clear-on-read, wrap-around, the storage size, the cached cursors refreshed on both slow paths, the cursors' cache lines), `ring_bench_test.go` (the numbers in `BENCH.md`), `perf_fixtures_test.go` (race off: the fixtures of the design's budgets on `Queue.TryWrite`, `Queue.TryRead`, `MultiQueue.TryWrite` and `MultiQueue.TryRead`, both paths of each allocating nothing — ADR 0165) and `gen_perf_test.go` (kit gen's, run by the alloc lane).
