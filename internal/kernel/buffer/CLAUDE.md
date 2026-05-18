<!-- updated: 2026-05-18T14:30:00Z -->
# internal/kernel/buffer/

## Purpose

Two complementary zero-alloc-reuse primitives for hot paths. Stdlib-only, domain-neutral. Code range `1200-1299` reserved (no sentinels emitted today). Logger v2 entries, async middleware, and any future codec stream / metrics writer can plug into either primitive without re-paying allocation cost.

## Contents

| Primitive | Surface | Use case |
|---|---|---|
| Byte pool | `Get() *[]byte` / `Put(*[]byte)` | scratch buffer for line / byte formatting (1024-byte default cap, 64-KiB drop ceiling) |
| `Recycler[T]` | `NewRecycler[T any](newFn func() T) Recycler[T]` returning `Get() T` / `Put(v T)` | recycle ANY pointer-sized typed object — event records, attr slices, scratch structs |

## Conventions

- **Pool stores `*[]byte`, not `[]byte`.** Avoids boxing the slice header on every trip. Callers `*bp = b` after growth to publish the resized slice back.
- **`Put(nil)` is a safe no-op.** Callers `defer Put(bp)` unconditionally.
- **Drop-on-oversize.** Byte buffers whose `cap > maxRetain` (64 KiB) are dropped on `Put` to keep pool memory bounded against rare large records.
- **`NewRecycler(nil)` returns nil.** The constructor refuses a nil factory rather than panicking on first cache miss. Factory MUST return a non-zero T — the pool re-caches verbatim.
- **IFACE-PLUGIN.** `Recycler[T]` is an interface backed by the unexported `objectBucket[T]`; the marker in `pool.go` lets ktn-linter recognise the swap-able backing.
- **Steady-state benchmark.** `BenchmarkRecyclerSteadyState` reports 0 allocs/op once the pool is warm — the contract the logger v2 zero-alloc claim depends on.

## Do NOT

- Keep a reference to a value after `Put`. The pool may hand it to another goroutine immediately.
- Call `Put` more than once on the same value.
- Use `Recycler[T]` for value types larger than a few words — `sync.Pool` boxes everything via `any`, so non-pointer-sized T pays an allocation on every `Put`.
- Add `Sleep`, timer helpers, or anything time-shaped here — that's `clock/`.

## Verification

```
bazel test --config=race //internal/kernel/buffer:buffer_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./buffer
# coverage target: 90%+
```

Tests: `buffer_external_test.go` (public contract: fresh length, oversize drop, nil-safety), `buffer_internal_test.go` (constants, `pool.New` capacity), `pool_external_test.go` (`NewRecycler` nil rejection, cold + warm paths, concurrent Get/Put under `-race`), `pool_internal_test.go` (`objectBucket.Get` happy + wrong-type fallback, `objectBucket.Put`).

A longer-form companion lives in `README.md` (typical-use snippets, benchmark numbers).
