# `internal/kernel/buffer`

**Layer**: kernel · **Code range**: 1200-1299 (reserved, no emissions today)

Two complementary primitives for zero-alloc reuse on hot paths. Stdlib-only.

| Primitive | Surface | When to reach for it |
|---|---|---|
| Byte pool | `Get() *[]byte` / `Put(*[]byte)` | scratch buffer for line/byte formatting |
| Recycler[T] | `NewRecycler[T any](newFn func() T) Recycler[T]` + `.Get() / .Put(v T)` | recycle ANY pointer-sized typed object (event records, attr slices, scratch structs) |

## Byte pool

`sync.Pool` of `[]byte` with a 1024-byte default capacity and a 64-KiB drop ceiling.

```go
func Get() (b *[]byte)   // length 0, cap ≥ 1024
func Put(b *[]byte)      // resets length to 0; drops when cap > 64 KiB; nil-safe
```

### Contract

- Buffers are returned with **zero length** and capacity ≥ `initialCap` (1024 bytes).
- `Put(nil)` is a safe no-op, so callers can `defer Put(bp)` unconditionally.
- Buffers that grew past `maxRetain` (64 KiB) are dropped on `Put` to keep pool memory bounded against rare large records.
- The pool stores `*[]byte` (not `[]byte`) to avoid boxing the slice header each trip; `Get` / `Put` traffic in pointers.

### Typical use

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

## Recycler[T]

A typed wrapper over `sync.Pool` so callers can recycle any pointer-sized object without paying the boxing cost on every `Get`/`Put`. The factory is invoked exactly once per cache miss; the returned value is re-cached when `Put` is called for the corresponding `Get`.

```go
type Recycler[T any] interface {
    Get() (v T)
    Put(v T)
}

func NewRecycler[T any](newFn func() T) Recycler[T]
```

### Contract

- `NewRecycler(nil)` returns nil — the constructor refuses a nil factory rather than panicking on the first cache miss.
- Values returned by `Get` MAY come from the pool or from a fresh factory invocation; callers MUST treat them as opaquely owned until they `Put` them back.
- `Put` re-caches the value verbatim — callers MUST NOT touch v after `Put` returns; the pool may hand it to another goroutine immediately.
- Implementations are safe for concurrent use by multiple goroutines.
- The factory MUST return a non-zero T (the pool re-caches whatever the factory produces verbatim).

### Typical use — pooling event records

```go
var recordPool = buffer.NewRecycler[*Record](func() *Record { return &Record{} })

func emit(lv Level, msg string) {
    r := recordPool.Get()
    defer recordPool.Put(r)

    r.Time = time.Now()
    r.Level = lv
    r.Message = msg

    handler.Handle(ctx, *r)
}
```

### Performance

Steady-state benchmark (`BenchmarkRecyclerSteadyState` on aarch64):

```
BenchmarkRecyclerSteadyState-8   2_538_306   483.8 ns/op   0 allocs/op
```

Zero heap allocations per `Get`+`Put` cycle once the pool is warm — the contract for the upper layers' zero-alloc claim.

## Do NOT

- Keep a reference to a value after `Put`. The pool may hand it to another goroutine the next instant.
- Call `Put` more than once on the same value.
- Use `Recycler[T]` for value types that are larger than a few words — sync.Pool boxes everything via `any`, so non-pointer-sized types pay an allocation on every `Put`.

## Tests

- `buffer_external_test.go` exercises the byte pool's public contract: fresh length, oversize dropping, nil-safety.
- `buffer_internal_test.go` asserts byte-pool constants and that `pool.New` always produces a `*[]byte` with the standard capacity.
- `pool_external_test.go` covers `NewRecycler` (nil rejection, cold path, warm path) and concurrent Get/Put under `-race`.
- `pool_internal_test.go` covers `objectBucket.Get` (happy path AND wrong-type fallback) and `objectBucket.Put`.
