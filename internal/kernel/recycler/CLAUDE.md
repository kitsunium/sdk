# internal/kernel/recycler/

## Purpose

The SDK's generic object-recycling primitive. Stdlib-only, domain-neutral.
`Pool[T]` wraps a `sync.Pool` to recycle ANY pointer-sized typed object
(event records, attr slices, scratch structs) without the boxing cost on
Get/Put. The byte-slice pool (`internal/kernel/buffer`) and the codec scratch
buffer pool (`internal/core/codec/scratch`) are built ON this primitive — the
mechanism lives here, the capacity thresholds stay with the consumers
(ADR 0010).

## Contents

| Primitive | Surface | Use case |
|---|---|---|
| `Pool[T]` | `NewPool[T any](newFn func() T)` → `Get() T` / `Put(v T)` | recycle typed objects; no reset |
| `CappedPool[T]` | `NewCappedPool[T any](newFn, resetFn, capOfFn, maxCap)` → `Get`/`Put` | adds reset-on-Put + cap-discard |

## Conventions

- **Pool[T] has no reset.** Consumers that need cleanup either reset
  before Put (logger `Builder.Send`, async `data[:0]`) or use
  `CappedPool[T]`.
- **CappedPool[T] is reset-on-Put + discard-before-reset.** Over-cap
  values are orphaned (never reset, never repooled) — this preserves the
  codec scratch detach contract. Reset is NOT a memory wipe.
- **Generic recyclers do NOT nil-check.** Public/package-level wrappers
  (`buffer.Put`, `scratch.ReleaseBuffer`) keep nil guards where nil is part
  of their accepted API — a generic `T any` cannot nil-check.
- **Pointer-like T only.** `sync.Pool` boxes everything via `any`; a
  non-pointer-sized T pays an allocation on every Put.

## Do NOT

- Keep a reference to a value after `Put`.
- Use `Pool[T]` for value types larger than a few words.
- Add worker pools, queues, metrics, policy objects, or lifecycle managers
  here — this is a primitive, not a framework (ADR 0010).

## Verification

```sh
bazel test --config=race //internal/kernel/recycler:recycler_test
# zero-alloc gate runs HORS race (race instrumentation perturbs allocs):
bazel test --config=pure //internal/kernel/recycler:recycler_test
```
