<!-- updated: 2026-11-03T22:00:00Z -->
# internal/kernel/concur/buffer/

## Purpose

A pooled `*[]byte` for zero-alloc formatting on hot paths. Stdlib-only,
domain-neutral. Since ADR 0010 this is a thin **byte-slice specialisation over
`recycler.CappedPool[*[]byte]`**: the recycling mechanism lives in
`internal/kernel/concur/recycler`; the size classes, their thresholds and the
`*[]byte` type live here. The generic `Pool[T]` that used to share this
package moved to `recycler`.

## Contents

| Primitive | Surface | Use case |
|---|---|---|
| Byte pool | `Get() *[]byte` / `GetSize(n) *[]byte` / `Put(*[]byte)` | scratch buffer for line / byte formatting (`Get`: the 1 KiB class) and a sized buffer (`GetSize`: the smallest class ≥ `n` — `net/server`'s read buffer) |

## Conventions

- **Pool stores `*[]byte`, not `[]byte`.** Avoids boxing the slice header into
  `sync.Pool`'s `any` payload on every trip — this is what keeps the hot path
  allocation-free. Callers `*bp = b` after growth to publish the resized slice.
- **`Put(nil)` is a safe no-op.** Callers `defer Put(bp)` unconditionally. The
  generic `CappedPool` cannot nil-check a pointer-like `T`, so the guard
  lives in `Put`.
- **Five size classes** (#810, ADR 0216): 1, 4, 16, 64 and 256 KiB, one
  `recycler.CappedPool` each (`pools`), whose factory makes exactly the class's
  size. `GetSize(n)` serves the smallest class that fits (`classFor`: ceil of
  log4 of `n` / 1 KiB); above 256 KiB the buffer is allocated to measure. A
  class never hands out a buffer smaller than its size.
- **`Put` routes by capacity, and drops past twice the class.** `classOf`
  picks the largest class a buffer's capacity covers; that class keeps it only
  while its capacity is at most twice its size — the recycler's
  discard-before-reset —, so a buffer grown past its class is dropped and no
  class pins more than twice what it hands out. A buffer below 1 KiB is left
  to the GC. The cost, said out loud: a 1 KiB `Get` buffer grown to between
  8 and 16 KiB (a long log record) is dropped where the old shared pool kept
  it, and reallocated next time.
- **`Put` stays inlinable.** The routing is `putClass`, out of line: `Put`
  inlined at base, and kit gen refuses a change that makes it stop.

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
after `Put`; oversize drop; nil-safety; `GetSize` never handing out less than
asked; #810's 70 / 128 KiB kept and 130 KiB dropped by the 64 KiB class),
`buffer_internal_test.go` (the classes' sizes, `classFor` and `classOf` at
every boundary, each class's factory), `buffer_bench_test.go` (warm
`Get`, the `Get`/write/`Put` round trip, and the round trip under
`RunParallel` — the numbers are in `BENCH.md`; there is no isolated `Put`
benchmark, because a `Put` without its `Get` measures pool growth). The
zero-alloc steady-state of `Get`/`Put` and of a 16 KiB `GetSize`/`Put` is the
design's budgets on `Get` and `GetSize` (`allocs: 0`, ADR 0165): `perf_fixtures_test.go` holds its fixture, and the
`gen_perf_test.go` kit gen writes counts it, race off.
