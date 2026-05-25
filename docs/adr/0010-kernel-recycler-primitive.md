# ADR 0010 — Kernel object-recycling primitive

## Status

Accepted

## Date

2026-05-25

## Deciders

kodflow (with multi-lens design review — see `.claude/plans/kernel-recycler.md`)

## Context

Three object-recycling mechanisms had grown in parallel and duplicated the
same cap-discard logic while keeping their capacity thresholds in different
layers:

| Mechanism | Layer | Type | Cap-discard | Reset |
|---|---|---|---|---|
| `kernel/buffer` `Get`/`Put` | kernel | `*[]byte` | 64 KiB, hand-rolled | on Put |
| `kernel/buffer` `Pool[T]` (interface) | kernel | `T` | none | none |
| `core/codec/scratch` `Acquire*`/`Release*` | core | `*bytes.Buffer` + `*bytes.Reader` | 256 KiB, hand-rolled | Acquire + Release |

The cap-discard *mechanism* is generic; only the *threshold* and the concrete
*type* are domain-specific. Three copies could (and did) drift.

## Decision

Introduce `internal/kernel/recycler`, a minimal kernel primitive for reusable
object recycling. It exposes **concrete types only — no public interface**:

- `Pool[T]` — owns a `sync.Pool`, performs **no reset**. Suitable for
  consumers that reset at their own boundary (logger `Builder.Send`, async
  `data[:0]`).
- `CappedPool[T]` — composes a concrete `*Pool[T]` and adds
  `reset func(T)` + `capOf func(T) int` + `maxCap int`.

Conventions:

- **reset-on-Put**, only in `CappedPool`.
- `CappedPool.Put` **discards oversized values BEFORE reset** — an over-cap
  value is orphaned (never reset, never repooled). This preserves the codec
  scratch detach contract, where an over-cap buffer's `Bytes()` has been handed
  to the caller and must not be touched. **Reset is not a memory wipe**; a
  consumer recycling sensitive bytes must zero them before Put.
- `NewPool(nil)` **panics** (programmer error). `NewCappedPool` panics
  on a nil reset/capOf or a non-positive `maxCap` (a non-positive threshold
  would orphan every value, silently turning the pool into a never-recycling
  allocator).
- `Pool.Get()` is **fail-loud** via comma-ok + panic (mirrors
  `core/codec/scratch.poolGet`): the pool is contractually homogeneous, so a
  wrong-typed entry is an invariant break that must surface, not hide.
- **Generic recyclers do NOT nil-check**; public/package-level wrappers
  (`buffer.Put`, `scratch.ReleaseBuffer`/`ReleaseReader`) keep nil guards where
  nil is part of their accepted API — a generic `T any` cannot nil-check.
- **Runtime-parameterized resets** (`bytes.Reader.Reset(src)`) stay
  consumer-side on Acquire, backed by a plain `Pool[T]`.

Capacity thresholds remain owned by the consumers: 64 KiB (`kernel/buffer`),
256 KiB (`core/codec/scratch`).

## Alternatives considered

- **Keep the primitives as interfaces (IFACE-PLUGIN), the prior kernel
  convention.** Rejected: a single-implementation interface in a kernel
  primitive is over-abstraction, and recycler is internal plumbing never
  re-exported in `pkg/v1` — the interface-for-consumers convention lives at the
  public boundary (`logger.Logger`, `codec.Codec`), not here. Concrete structs
  also avoid an interface dispatch on the `Get`/`Put` hot path (exact ns gain
  left to a benchmark; the 0-alloc invariant holds either way).
- **Name the types `Recycler` / `CappedRecycler`.** Rejected on lint grounds:
  `recycler.CappedRecycler` is a genuine compound stutter — ktn-linter#359
  ruled this **by-design** (`KTN-STRUCT-ROLE-STUTTER` is doing its job), so
  carrying those names would force a permanent `.ktn-linter.yaml` exclusion.
  Renamed to the `sync.Pool`-idiomatic `Pool[T]` / `CappedPool[T]` instead:
  "Pool" is a recognized role suffix, so both types pass `KTN-STRUCT-ROLE` and
  `KTN-STRUCT-ROLE-STUTTER` **natively — zero exclusion**, and independent of
  the PNPT fix (kodflow/ktn-linter#356), so it works on the released v1.34.0
  binary (verified). The package stays `recycler` (the mechanism); the types are
  pools, exactly like `sync.Pool`.
- **A `worker` pool sibling now.** Deferred: a worker pool composes `ring`, not
  `recycler`, and has only one plausible consumer today (async logger). Built
  on demand, not ahead of it (no empty package).
- **Name the package `pool`.** Rejected: `recycler` names the actual mechanism
  (reuse + reset + cap-discard) and blocks the natural drift toward stuffing a
  worker pool into a generic `pool` package.

## Consequences

- `kernel/buffer` becomes a `recycler.CappedPool[*[]byte]` (64 KiB) wrapper;
  `*[]byte` and the nil-safe `Put` no-op stay.
- `core/codec/scratch` uses `recycler.CappedPool[*bytes.Buffer]` (256 KiB)
  for buffers and a plain `recycler.Pool[*bytes.Reader]` for readers; the
  `poolGet` helper is gone. The "Why core, not kernel" rationale is revised —
  only the mechanism migrated; the 256 KiB codec threshold stays in core.
- `service/logger` (recordPool) and `service/logger/middleware/async` (sink
  pool) consume `recycler.Pool`; reset stays consumer-side.
- The IFACE-PLUGIN convention (kernel exported types are interfaces) no longer
  applies to `recycler`: it is the first kernel primitive shipped as concrete
  structs by deliberate choice.
- recycler emits no dotted-quad error codes (it panics on programmer error;
  panics are not returned errors, so ADR 0005 has no entry).
- `worker` remains a deferred sibling until at least two concrete consumers
  exist.

## References

- `.claude/plans/kernel-recycler.md` — design + 10-lens refinement
- `internal/kernel/CLAUDE.md` — kernel gate (stdlib-only AND generic)
- ADR 0005 — error codes (recycler emits none)
- Tracking issue #32 · Refs #18 (kernel/buffer benches), #23 (zero-alloc gate)
