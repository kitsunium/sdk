<!-- updated: 2026-05-25T00:00:00Z -->
# internal/kernel/buffer/

## Purpose

A pooled `*[]byte` for zero-alloc formatting on hot paths. Stdlib-only,
domain-neutral. Since ADR 0010 this is a thin **byte-slice specialisation over
`recycler.CappedRecycler[*[]byte]`**: the recycling mechanism lives in
`internal/kernel/recycler`; the 64-KiB threshold and the `*[]byte` type live
here. The generic `Recycler[T]` that used to share this package moved to
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
  generic `CappedRecycler` cannot nil-check a pointer-like `T`, so the guard
  lives in `Put`.
- **Drop-on-oversize.** Buffers whose `cap > maxRetain` (64 KiB) are dropped on
  `Put` — implemented as the recycler's discard-before-reset.

## Do NOT

- Keep a reference to a buffer after `Put`. The pool may hand it to another
  goroutine immediately.
- Reach for a typed object pool here — that is `recycler.Recycler[T]` /
  `recycler.CappedRecycler[T]`.
- Add `Sleep`, timer helpers, or anything time-shaped here — that's `clock/`.

## Verification

```
bazel test --config=race //internal/kernel/buffer:buffer_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./buffer
# coverage target: 90%+
```

Tests: `buffer_external_test.go` (public contract: fresh length, oversize drop,
nil-safety), `buffer_internal_test.go` (constants, fresh-buffer capacity via the
package recycler). The zero-alloc steady-state of `Get`/`Put` is gated by
`recycler`'s `TestZeroAllocInvariant` (race-off).

A longer-form companion lives in `README.md`.
