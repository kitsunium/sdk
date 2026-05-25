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
| `kernel/buffer` `Recycler[T]` (interface) | kernel | `T` | none | none |
| `core/codec/scratch` `Acquire*`/`Release*` | core | `*bytes.Buffer` + `*bytes.Reader` | 256 KiB, hand-rolled | Acquire + Release |

The cap-discard *mechanism* is generic; only the *threshold* and the concrete
*type* are domain-specific. Three copies could (and did) drift.

## Decision

Introduce `internal/kernel/recycler`, a minimal kernel primitive for reusable
object recycling. It exposes **concrete types only — no public interface**:

- `Recycler[T]` — owns a `sync.Pool`, performs **no reset**. Suitable for
  consumers that reset at their own boundary (logger `Builder.Send`, async
  `data[:0]`).
- `CappedRecycler[T]` — composes a concrete `*Recycler[T]` and adds
  `reset func(T)` + `capOf func(T) int` + `maxCap int`.

Conventions:

- **reset-on-Put**, only in `CappedRecycler`.
- `CappedRecycler.Put` **discards oversized values BEFORE reset** — an over-cap
  value is orphaned (never reset, never repooled). This preserves the codec
  scratch detach contract, where an over-cap buffer's `Bytes()` has been handed
  to the caller and must not be touched. **Reset is not a memory wipe**; a
  consumer recycling sensitive bytes must zero them before Put.
- `NewRecycler(nil)` **panics** (programmer error). `NewCappedRecycler` panics
  on a nil reset/capOf or a non-positive `maxCap` (a non-positive threshold
  would orphan every value, silently turning the pool into a never-recycling
  allocator).
- `Recycler.Get()` is **fail-loud** via comma-ok + panic (mirrors
  `core/codec/scratch.poolGet`): the pool is contractually homogeneous, so a
  wrong-typed entry is an invariant break that must surface, not hide.
- **Generic recyclers do NOT nil-check**; public/package-level wrappers
  (`buffer.Put`, `scratch.ReleaseBuffer`/`ReleaseReader`) keep nil guards where
  nil is part of their accepted API — a generic `T any` cannot nil-check.
- **Runtime-parameterized resets** (`bytes.Reader.Reset(src)`) stay
  consumer-side on Acquire, backed by a plain `Recycler[T]`.

Capacity thresholds remain owned by the consumers: 64 KiB (`kernel/buffer`),
256 KiB (`core/codec/scratch`).

## Alternatives considered

- **Keep `Recycler[T]` as an interface (IFACE-PLUGIN), the prior kernel
  convention.** Rejected: a single-implementation interface in a kernel
  primitive is over-abstraction; the concrete struct sharpens inlining on the
  zero-alloc hot path. ktn-linter's `KTN-STRUCT-ROLE` flags the exported
  struct (its name has no role suffix); a single-concept primitive whose name
  IS its API (cf. stdlib `ring.Ring` / `list.List`) is the documented exception
  — scoped out in `.ktn-linter.yaml`, the same way `kernel/ring` scopes the
  generic-type-parameter false positives.
- **A `worker` pool sibling now.** Deferred: a worker pool composes `ring`, not
  `recycler`, and has only one plausible consumer today (async logger). Built
  on demand, not ahead of it (no empty package).
- **Name the package `pool`.** Rejected: `recycler` names the actual mechanism
  (reuse + reset + cap-discard) and blocks the natural drift toward stuffing a
  worker pool into a generic `pool` package.

## Consequences

- `kernel/buffer` becomes a `recycler.CappedRecycler[*[]byte]` (64 KiB) wrapper;
  `*[]byte` and the nil-safe `Put` no-op stay.
- `core/codec/scratch` uses `recycler.CappedRecycler[*bytes.Buffer]` (256 KiB)
  for buffers and a plain `recycler.Recycler[*bytes.Reader]` for readers; the
  `poolGet` helper is gone. The "Why core, not kernel" rationale is revised —
  only the mechanism migrated; the 256 KiB codec threshold stays in core.
- `service/logger` (recordPool) and `service/logger/middleware/async` (sink
  pool) consume `recycler.Recycler`; reset stays consumer-side.
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
