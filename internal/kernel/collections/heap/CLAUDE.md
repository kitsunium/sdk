# internal/kernel/collections/heap/

## Purpose

Generic **priority queue**: `Heap[T any]` is a binary heap ordered by a
comparison function the caller supplies. A kernel primitive (stdlib-only AND
generic — one type parameter and one comparison, with **no `Job`, no `Task`, no
`Priority`** anywhere in the API). Admitted on **SDK rule 1**, the only
admission criterion there is; the precedent is ADR 0025, which admitted
`kernel/collections/cache` on that rule with **zero** consumers.

**Published as `pkg/v1/collections/heap`** (ADR 0159 §4): a pure alias, so every exported shape here is public API — a renamed field, a changed signature or a new method on an exported interface breaks a consumer at compile time, allowed only while the module is v0 and said out loud (ADR 0040).

Emits **no error codes**: an empty heap returns `(zero, false)`, and the one
programmer error it can detect panics.

## Contents

| File | Surface |
|---|---|
| `heap.go` | `Heap[T]` + `New` / `Len` / `Push` / `Peek` / `Pop` / `Drain` (Pop's loop as an `iter.Seq[T]`, #816), and the `up` / `down` sifts |
| `indexed.go` | `IndexedHeap[T]` + `NewIndexed` / `Len` / `Push` / `Peek` / `Pop` / `Update` / `Remove`, and `Handle` — the sibling with a position table (#1036) |

One file per type, on purpose: a binary heap is ~80 lines of algorithm and
splitting one would put the two halves of one invariant in two places.

## `IndexedHeap` — the sibling with handles (#1036)

`Heap[T]` is published (`pkg/v1/collections/heap`) and FROZEN at Push, Pop,
Peek and Len (ADR 0159 §4, ADR 0040), so re-keying and removal arrive as a
SIBLING type rather than as methods on it — they would change what Push
returns and what every element costs. `Drain` (#816) is additive: Pop's loop
as an iterator, no field and no signature changed, 0 allocations. `IndexedHeap[T]` returns a `Handle`
from every `Push`; `Update(h, v)` replaces the element and sifts it whichever
way the order asks, `Remove(h)` takes it out from anywhere — both O(log n),
what `container/heap`'s `Fix` and `Remove` do. Without them every consumer
that reschedules or cancels re-implemented lazy deletion (the statemachine's
agenda, the memory queue broker's lease expiries).

- **A position table, ids recycled.** Each array cell carries its handle's id;
  `slots[id]` holds where the cell sits, kept in step by every swap. A popped
  or removed id goes on a free list and the next `Push` takes it, so a heap in
  steady state allocates nothing — a design budget (`design/sdk.yaml`,
  `perf_fixtures_test.go`, race off: the alloc lane).
- **A generation per id.** A `Handle` is `(id, gen)`; `Push` bumps the slot's
  generation, so a handle whose element left names nothing even after its id
  is reissued — `Update` reports false and `Remove` reports `(zero, false)`
  (`TestIndexedStaleHandleNamesNothing`). Generations start at 1, so the zero
  `Handle` never names anything: a caller stores one in a struct and reads
  "not scheduled" from it.
- **The vacated cell is cleared** on `Pop` and `Remove`, as `Heap.Pop` does —
  `TestIndexedRemoveFromTheMiddleKeepsTheInvariant` reads the array beyond
  `len`.
- **Kernel only.** It is not aliased into `pkg/v1/collections/heap`; publishing
  it is a separate decision.

## Why not `container/heap`

`container/heap` exists and cannot be used cleanly here. It is an **algorithm
over an interface the caller must implement**: five methods (`Len`, `Less`,
`Swap`, `Push`, `Pop`) on a named slice type, where `Push` takes `any` and `Pop`
returns `any`. Every use site writes the same boilerplate, boxes each element on
the way in, and type-asserts it on the way out — a conversion the compiler
cannot check and the reader has to trust.

That is not a criticism; it predates type parameters by a decade. It is measured
rather than asserted: `BENCH.md` runs both over the same input and the interface
costs **1.75× the time and one allocation per push** (16 B, forever, for a value
that fits in a register).

## The comparison, and ADR 0031

`New(cmp)` takes `func(a, b T) int`, following the `cmp.Compare` /
`slices.SortFunc` convention: negative when `a` sorts before `b`. **The element
that sorts FIRST is on top**, so `cmp.Compare[int]` gives a min-heap and a
max-heap is the same function with its arguments reversed — no second type, no
flag.

**A nil `cmp` panics, at the call that made the mistake.** This is ADR 0031's
*refusal* branch rather than its *clamp* branch, and the line between them is
exactly the ADR's: clamp when a working default exists that a reader would
accept without being told the number; refuse when any value chosen on the
caller's behalf would be arbitrary. There is no order the SDK can invent for an
arbitrary `T`, so there is nothing to clamp to.

The refusal is a panic rather than an error for two reasons. The kernel has no
error budget for programmer faults — `recycler` and `clock` take the same
position, both documented as "panics on programmer error" in
`internal/kernel/CLAUDE.md`. And the alternative, a `Heap` that panics on the
first `Push`, moves the crash away from the line that caused it. (ADR 0031
rejected panicking for the *resilience* policies, on the grounds that crashing a
process over one local misconfiguration is disproportionate for a library whose
subject is keeping services up. That reasoning does not transfer: a nil
comparison is not a misconfiguration with a survivable degraded mode, it is a
heap that cannot order anything.)

## Conventions

- **`Pop` and `Peek` return `(T, bool)`.** Emptiness is an ordinary state of a
  queue, not a programmer error — the same shape `kernel/collections/cache.Fetch` uses for a
  miss, and the reason this package needs no sentinel and no error code.
- **`Peek` really is a getter.** Unlike `cache.Fetch`, reading the top mutates
  nothing, so the pure-accessor name is honest here.
- **Ties are unordered.** A binary heap is not stable and no care in the
  comparison makes it so. What IS guaranteed is that no element is lost or
  duplicated — `TestTiesComeOutInAnUnspecifiedOrderButAllOfThem`.
- **`Pop` clears the vacated slot.** Without it, a heap of pointers keeps the
  popped element reachable for as long as the backing array lives — a leak no
  black-box test can see, so `TestPopClearsTheVacatedSlot` reads the array
  beyond `len`.
- **`New` allocates nothing.** The backing array appears on the first `Push`.
- **Not safe for concurrent use**, deliberately: a lock inside would be paid by
  every caller, including the many holding a heap inside a structure they
  already serialise.
- Cross-OS: 100 % portable (no imports at all in the production file).

## Do NOT

- Add a `Priority`, a `Job`, a deadline or a key to the API. The comparison is
  the caller's, and the moment a domain word enters a signature this package
  stops being a kernel primitive (SDK rule 1 — the reason `level` was moved
  out).
- Add an internal mutex. See above; the caller picks the lock.
- Return a sentinel or an error for an empty heap. `(T, bool)` is the SDK's
  spelling and costs no error-code range.
- Assume stability. If a caller needs FIFO among ties, they add a sequence
  number to `T` and compare on it — which the generic comparison makes trivial
  and this package must not decide for them.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=10 ./collections/heap/
bazel test --config=race //internal/kernel/collections/heap:heap_test
```
