<!-- updated: 2026-10-03T23:13:39Z -->
# internal/kernel/concur/buffer/

## Purpose

A pooled `*[]byte` for zero-alloc formatting on hot paths. Stdlib-only,
domain-neutral. Since ADR 0010 this is a thin **byte-slice specialisation over
`recycler.CappedPool[*[]byte]`**: the recycling mechanism lives in
`internal/kernel/concur/recycler`; the 64-KiB threshold and the `*[]byte` type live
here. The generic `Pool[T]` that used to share this package moved to
`recycler`.

## Contents

| Primitive | Surface | Use case |
|---|---|---|
| Byte pool | `Get() *[]byte` / `Put(*[]byte)` | scratch buffer for line / byte formatting (1024-byte default cap, 64-KiB drop ceiling) |

## Conventions

- **Pool stores `*[]byte`, not `[]byte`.** Avoids boxing the slice header into
  `sync.Pool`'s `any` payload on every trip — this is what keeps the hot path
  allocation-free. Callers `*bp = b` after growth to publish the resized slice.
- **`Put(nil)` is a safe no-op.** Callers `defer Put(bp)` unconditionally. The
  generic `CappedPool` cannot nil-check a pointer-like `T`, so the guard
  lives in `Put`.
- **Drop-on-oversize.** Buffers whose `cap > maxRetain` (64 KiB) are dropped on
  `Put` — implemented as the recycler's discard-before-reset.

## Do NOT

- Touch a buffer after `Put` — not even to read `len(*b)` — or `Put` it twice.
  `Put` ends the caller's ownership, and the pool may hand the buffer to
  another goroutine immediately, which writes `*b` and its bytes as its own.
  Copy out what is still needed first. A test reading `len(*b)` after `Put`
  raced a parallel `TestGet`'s cleanup `Put` (seen once in CI, reproduced
  under load); a test observes the reset on the buffer `Get` hands back.
- Reach for a typed object pool here — that is `recycler.Pool[T]` /
  `recycler.CappedPool[T]`.
- Add `Sleep`, timer helpers, or anything time-shaped here — that's `clock/`.

## Verification

```
bazel test --config=race //internal/kernel/concur/buffer:buffer_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./concur/buffer
# coverage target: 90%+
```

Tests: `buffer_external_test.go` (public contract: fresh length; the reset,
checked on the dirtied buffer once `Get` hands it back, never by reading it
after `Put`; oversize drop; nil-safety), `buffer_internal_test.go` (constants,
fresh-buffer capacity via the package recycler), `buffer_bench_test.go` (warm
`Get`, the `Get`/write/`Put` round trip, and the round trip under
`RunParallel` — the numbers are in `BENCH.md`; there is no isolated `Put`
benchmark, because a `Put` without its `Get` measures pool growth). The
zero-alloc steady-state of `Get`/`Put` is the design's budget on `Get`
(`allocs: 0`, ADR 0165): `perf_fixtures_test.go` holds its fixture, and the
`perf_gen_test.go` kit gen writes counts it, race off.

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declaration of ; `Get` and `Put`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name.
