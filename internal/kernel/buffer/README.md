# `internal/kernel/buffer`

**Layer**: kernel · **Code range**: 1200-1299 (reserved, no emissions today)

A `sync.Pool` of `[]byte` for zero-alloc formatting in hot logging paths. Stdlib-only. Use it when you need a scratch buffer that is almost always recycled and occasionally dropped for being too large.

## Surface

```go
func Get() (b *[]byte)   // length 0, cap ≥ 1024
func Put(b *[]byte)      // resets length to 0; drops when cap > 64 KiB; nil-safe
```

## Contract

- Buffers are returned with **zero length** and capacity ≥ `initialCap` (1024 bytes).
- `Put(nil)` is a safe no-op, so callers can `defer Put(bp)` unconditionally.
- Buffers that grew past `maxRetain` (64 KiB) are dropped on `Put` to keep pool memory bounded against rare large records.
- The pool stores `*[]byte` (not `[]byte`) to avoid boxing the slice header each trip; `Get` / `Put` traffic in pointers.

## Typical use

```go
bp := buffer.Get()
defer buffer.Put(bp)
b := *bp

b = append(b, "INFO "...)
b = append(b, msg...)
b = append(b, '\n')
*bp = b  // publish the grown slice back for the pool to reuse

w.Write(b)
```

## Do NOT

- Keep a reference to the underlying slice after `Put`. The pool may hand it to another goroutine the next instant.
- Call `Put` more than once on the same pointer.
- Assume capacity invariants beyond `≥ initialCap` — the pool may return a freshly allocated buffer at any time.

## Tests

- `buffer_external_test.go` exercises the public contract: fresh length, oversize dropping, nil-safety.
- `buffer_internal_test.go` asserts constants and that `pool.New` always produces a `*[]byte` with the standard capacity.
