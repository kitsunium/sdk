# internal/kernel/snapshot/

## Purpose

The SDK's copy-on-write container primitive. Stdlib-only, domain-neutral.
`Value[T]` wraps `atomic.Pointer[T]` so readers `Load` the current snapshot
lock-free and allocation-free, while writers (`Store` / `Swap` / `Update`)
serialise on a mutex for a race-free read-modify-write publish. The codec
registry (`internal/core/codec`) is built ON this primitive — the mechanism
lives here, the domain clone logic stays with the consumer (ADR 0011).

## Contents

| Primitive | Surface | Use case |
|---|---|---|
| `Value[T]` | `NewValue[T any](initial *T)` → `Load` / `Store` / `Swap` / `Update` | read-mostly shared state: registries, routing tables, hot-reloaded config |

## Conventions

- **Load is the hot path.** One `atomic.Pointer` load — zero alloc, no lock.
  The returned `*T` is shared with every other reader and MUST be treated as
  immutable: mutate a clone and publish it via `Store` / `Update`.
- **Writers serialise on a mutex.** `Store` / `Swap` / `Update` take the lock
  so a read-modify-write (`Update`) cannot lose a concurrent write. Readers
  never take the lock.
- **The zero value is usable.** `var v Value[T]` reads nil until the first
  `Store`; `NewValue` is call-site sugar when an initial value is known.
- **Concrete struct, no interface.** Like `recycler` (ADR 0010), snapshot ships
  a concrete struct by deliberate choice — a single-impl interface would be
  over-abstraction (ADR 0011). The `Value` role suffix passes `KTN-STRUCT-ROLE`
  natively.

## Do NOT

- Mutate the value returned by `Load` — other goroutines hold the same pointer.
- Call `Store` / `Swap` / `Update` from inside an `Update` fn — it deadlocks.
- Use it for write-heavy state — every write clones. It is a read-mostly COW
  container, not a concurrent map (reach for `sync.Map` if writes dominate).

## Verification

```sh
bazel test --config=race //internal/kernel/snapshot:snapshot_test
# zero-alloc gate runs HORS race (race instrumentation perturbs allocs):
bazel test --config=pure //internal/kernel/snapshot:snapshot_test
```
