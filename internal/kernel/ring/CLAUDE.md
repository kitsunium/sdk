<!-- updated: 2026-05-18T14:30:00Z -->
# internal/kernel/ring/

## Purpose

Lock-free single-producer / single-consumer (SPSC) bounded queue built on `sync/atomic`. Admitted to the kernel by ADR 0006 — domain-neutral by construction (any future metrics batcher, codec stream, event bus, or hot-path FIFO can plug in). Today the only consumer is `internal/service/logger/middleware/async`; the surface is generic so other domains can adopt without a refactor. Code range `0x00_01_03_*` (`0.1.3.*`).

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

// Sentinels (errs.Define-backed, kernel-range codes).
var Full    *errs.Error // 0.1.3.1 RING_FULL
var Empty   *errs.Error // 0.1.3.2 RING_EMPTY
var CapZero *errs.Error // 0.1.3.3 RING_CAP_ZERO
```

## Conventions

- **SPSC contract is strict.** EXACTLY ONE goroutine calls `TryWrite` and EXACTLY ONE calls `TryRead`. Concurrent producers OR concurrent consumers are NOT safe — the caller serialises upstream (logger's async middleware takes a mutex) or wraps this primitive externally.
- **Backing storage is `cap+1` slots.** One slot is left empty so `head == tail` unambiguously means "empty"; `next == head` after increment means "full". Wrap-around uses `% (cap+1)`.
- **Zero-alloc hot path.** `TryWrite` / `TryRead` return pre-allocated sentinel pointers (`Full`, `Empty`); no error is constructed at runtime. `errors.Is(err, ring.Full)` still works because the sentinels are stable package-level vars.
- **Slot cleared on read.** After dequeue the slot is zeroed (`b.slots[head] = zero`) so the GC can reclaim the item — no goroutine-private references survive past `TryRead`.
- **`IFACE-PLUGIN` marker** on `New` so the kernel can swap the backing implementation (single-buffer today, sharded SPMC tomorrow) without breaking call-site code.
- **`allocateSlots[T]` helper.** Wraps the `make([]T, n)` so the slice-growth audit does not misclassify this fixed-length, indexed-only backing array.

## Sentinels

| Var | Code | Reason | Returned by |
|---|---|---|---|
| `Full` | `0x00_01_03_01` (0.1.3.1) | `RING_FULL` | `TryWrite` on saturated ring |
| `Empty` | `0x00_01_03_02` (0.1.3.2) | `RING_EMPTY` | `TryRead` on empty ring |
| `CapZero` | `0x00_01_03_03` (0.1.3.3) | `RING_CAP_ZERO` | `New` when `capacity <= 0` |

Match with `errs.HasCode(err, ring.CodeRingFull)` or `errors.Is(err, ring.Full)`.

## Do NOT

- Share a single `Queue[T]` across multiple producers or multiple consumers without external serialisation. The SPSC contract is a hard precondition, not a hint.
- Spin in a tight loop on `TryRead` returning `Empty` without backoff — the consumer should wait on a wake signal from the producer (the async middleware uses a condvar).
- Construct sentinels at the call site (`errs.Define(...)`) — reuse `ring.Full / Empty / CapZero` so identity-based comparisons stay zero-alloc.
- Add `Blocking` / `Bounded` variants here without an ADR — admitting a second concurrency contract to the kernel is a layer-policy decision, not a drive-by.

## Verification

```
bazel test --config=race //internal/kernel/ring:ring_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./ring
# coverage target: 90%
```

Tests: `ring_external_test.go` (public contract: TryWrite/TryRead happy paths, full / empty sentinels, capacity reporting, Len snapshot, FIFO order, single-producer/single-consumer race coverage), `ring_internal_test.go` (cursor arithmetic, slot-clear-on-read, wrap-around at `cap+1`).
