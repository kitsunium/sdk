# ADR 0011 — Kernel copy-on-write snapshot primitive

## Status

Accepted

## Date

2026-05-25

## Deciders

kodflow

## Context

The codec registry (`internal/core/codec/registry.go`) hand-rolls a
copy-on-write snapshot across **three** `atomic.Pointer[map[K]V]` fields
(`registry`, `mimeIndex`, `extIndex`), each with its own CAS-retry writer
(`publishCodec`, `publishAlias`) and clone helper — roughly 130 lines of
plumbing plus two `//nolint:gocyclo` suppressions, because the CAS-retry shape
inflates cyclomatic complexity. The pattern is read-mostly: codec packages
register once at import, and every other access is a lock-free `Load`.

That copy-on-write mechanism is generic, stdlib-only, and domain-neutral — any
frozen-after-init or rarely-mutated table (a routing table, a feature-flag map,
a hot-reloaded config) wants exactly the same shape. It is the same
"duplicated mechanism, scattered thresholds" situation that justified the
`recycler` primitive (ADR 0010): the mechanism deserves one home, the domain
clone logic stays with the consumer.

## Decision

Introduce `internal/kernel/snapshot`, a minimal kernel primitive for
read-mostly shared state. Concrete type only (no interface), matching the
`recycler` precedent:

- `Value[T any]` — a copy-on-write container over `atomic.Pointer[T]`.
  - `Load() *T` is lock-free and allocation-free (one atomic load); the
    returned `*T` is a shared immutable snapshot.
  - `Store` / `Swap` / `Update` serialise on a mutex so a read-modify-write
    publish cannot lose a concurrent write. Readers never take the lock.
  - `Update(fn func(current *T) (next *T))` is the read-modify-write helper;
    returning the unchanged current pointer is a no-op publish (conditional
    abort).

Conventions:

- The zero value is usable — `Load` returns nil until the first `Store`; `New`
  is call-site sugar when an initial value is known. A nil initial is a valid
  empty container, not a programmer error (so, unlike `recycler.NewPool`,
  construction does not panic).
- Concrete struct by deliberate choice; a single-impl interface would be
  over-abstraction. The `Value` role suffix passes `KTN-STRUCT-ROLE` natively.
- The mechanism lives in kernel; the consumer keeps its domain clone logic
  (`cloneFormatMap` / `cloneAliasMap` stay in `core/codec`).

The codec registry is consolidated onto `Value[T]` in the same change so the
primitive ships with a real consumer (no empty package). Its three
`atomic.Pointer[map]` fields become three `snapshot.Value[map]`; the CAS-retry
loops and both `//nolint:gocyclo` suppressions are removed — `Update` makes the
conflict-check-and-publish atomic under the writer lock, while readers keep the
lock-free `Load` fast path.

No worker, queue, metrics, policy object, lifecycle manager, or `CompareAndSwap`
is introduced. `snapshot` emits no dotted-quad error codes (it never returns
errors and does not panic on use), so ADR 0005 has no entry.

## Alternatives considered

- **`sync.Map`** — not generic (forces `any` + type assertions on every read),
  and its dirty-map write path adds machinery for write-mostly workloads the
  registry never triggers. The registry microbench measured the
  `atomic.Pointer` snapshot at 9.1 ns vs 12.8 ns for `sync.Map.Load` + the
  assertion. Rejected for read-heavy frozen-after-init tables.
- **`sync.RWMutex` + plain map** — every reader takes the read lock (a counter
  CAS), defeating the lock-free read path that motivates the primitive.
- **Raw `atomic.Pointer[T]` at each call site** — this is exactly the
  hand-rolled CAS-loop boilerplate (×3 in the registry) the primitive
  consolidates.
- **Value API (`Store(T)` / `Load() (T, bool)`) backed by an internal box** —
  avoids exposing `*T`, but allocates a box on every `Store` and copies `T` on
  every `Load`. Rejected in favour of the pointer API, which is zero-alloc on
  all operations and mirrors the stdlib `sync/atomic.Pointer[T]` shape.

## Consequences

- `internal/core/codec` becomes a consumer of `snapshot.Value[map]` for its
  registry and the two alias indexes; ~130 lines of CAS plumbing and two
  `//nolint:gocyclo` suppressions are removed.
- The kernel gains a fourth generic primitive (`errs`, `recycler`, `ring`,
  `clock` … now `snapshot`); reads stay lock-free, writes stay correct.
- `internal/core/codec/CLAUDE.md` is revised: the "atomic.Pointer + CAS-loop"
  rationale becomes "consumes `snapshot.Value`; the mechanism lives in kernel".

## Breaking changes

None. This is an `internal/`-only change; `pkg/v1/*` exported API is untouched,
and the codec registry's observable behaviour (idempotent re-registration,
duplicate-Name/MIME/extension panic, lock-free lookups) is preserved verbatim.

## Deferred

- Additional consumers (routing tables, feature-flag maps, hot-reloaded config)
  adopt `Value[T]` as they appear; each is a one-liner, not new mechanism.
- A `CompareAndSwap` method is omitted until a lock-free-CAS consumer needs it;
  the mutex-serialised `Update` covers the read-modify-write case today.

## References

- [ADR 0010 — kernel object-recycling primitive](./0010-kernel-recycler-primitive.md) (sibling concrete-struct primitive)
- `internal/kernel/CLAUDE.md` — kernel gate (stdlib-only AND generic)
- `.claude/contexts/kernel-perf-primitives.md` — the primitive survey that ranked this Tier A
- ADR 0005 — error codes (snapshot emits none)
- Tracked upstream linter false positives suppressed for this primitive:
  `kodflow/ktn-linter#365` (`KTN-VAR-PTRINTF` on `*T`), `kodflow/ktn-linter#366`
  (`KTN-API-MINIF` on a concrete generic primitive)
