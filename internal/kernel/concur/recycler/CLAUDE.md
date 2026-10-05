<!-- updated: 2026-10-03T11:00:00Z -->
# internal/kernel/concur/recycler/

## Purpose

The SDK's generic object-recycling primitive. Stdlib-only, domain-neutral.
`Pool[T]` wraps a `sync.Pool` to recycle ANY pointer-sized typed object
(event records, attr slices, scratch structs) without the boxing cost on
Get/Put. The byte-slice pool (`internal/kernel/concur/buffer`) and the codec scratch
buffer pool (`internal/core/data/codec/scratch`) are built ON this primitive — the
mechanism lives here, the capacity thresholds stay with the consumers
(ADR 0010).

**Published as `pkg/v1/concur/recycler`** (ADR 0159 §4): a pure alias, so every exported shape here is public API — a renamed field, a changed signature or a new method on an exported interface breaks a consumer at compile time, allowed only while the module is v0 and said out loud (ADR 0040).

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
bazel test --config=race //internal/kernel/concur/recycler:recycler_test
# the zero-alloc contracts (design/sdk.yaml budgets, ADR 0165) run race off —
# perf_gen_test.go over perf_fixtures_test.go, in kit's section of the alloc
# lane (`make test-alloc`, tools/alloc-lane-targets.txt):
bazel test --config=alloc //internal/kernel/concur/recycler:recycler_test
```

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declarations of `CappedPool` and `Pool` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names.
