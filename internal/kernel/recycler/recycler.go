// Package recycler provides Pool[T any], a generic typed object pool
// backed by sync.Pool. Callers recycle ANY pointer-sized object (event
// records, attribute slices, scratch structs) without paying the boxing cost
// on Get/Put. Stdlib-only and domain-neutral: any byte buffer, codec stream,
// HTTP body encoder, or metrics line writer can reuse it.
//
// "Pointer-sized" is load-bearing, not decoration. sync.Pool stores any, and
// only a value that fits in an interface word is boxed for free — so a
// *[]byte costs nothing to Put while a bare []byte, being a three-word
// header, heap-allocates 24 B on every single Put. Measured in BENCH.md at
// 25.58 ns and 0 allocs against 82.95 ns and 1 alloc for the same workload,
// and 2.975 ns against 27.17 ns under b.RunParallel. Pool a pointer. Every
// consumer in this repository already does.
//
// The byte-slice pool (internal/kernel/buffer) and the codec scratch buffer
// pool (internal/core/codec/scratch) are built ON this primitive; Pool[T]
// is the shared mechanism, the capacity thresholds stay with the consumers.
package recycler

import "sync"

// Pool is a concrete generic object pool over sync.Pool. It performs NO
// reset — consumers that need cleanup either reset before Put (logger
// Builder.Send, async data[:0]) or use CappedPool. Safe for concurrent
// use. Always used behind a pointer: sync.Pool must not be copied.
type Pool[T any] struct {
	// pool is the underlying sync.Pool holding the recycled values.
	pool sync.Pool
}

// NewPool returns a Pool[T] whose factory fires on every cache miss.
// A nil factory is a programmer error and panics at construction rather than
// deferring the panic to sync.Pool.Get on the first cache miss, far from the
// offending call site.
func NewPool[T any](newFn func() T) *Pool[T] {
	//: refuse a nil factory loudly at construction, not on first Get.
	if newFn == nil {
		//: programmer error — fail fast at the call site.
		panic("recycler: nil factory")
	}
	//: build the recycler with a thin closure boxing the factory output for sync.Pool.
	r := &Pool[T]{}
	r.pool.New = func() any {
		//: invoke the caller's factory and hand its result to sync.Pool as any.
		return newFn()
	}
	//: hand the concrete recycler back to the caller.
	return r
}

// Get borrows a value of type T, invoking the factory on a cache miss. The
// returned value MAY be a previously Put value or a freshly built one. The
// comma-ok assertion is fail-loud: the pool only ever holds T (both New and
// Put are typed), so a wrong-typed entry is an internal invariant break that
// panics rather than silently masking it (mirrors core/codec/scratch.poolGet).
func (r *Pool[T]) Get() T {
	//: the pool only ever holds T — comma-ok guards the invariant.
	v, ok := r.pool.Get().(T)
	//: a wrong-typed entry is impossible by contract; fail loud if it happens.
	if !ok {
		//: never mask a broken pool invariant behind a zero value.
		panic("recycler: pool yielded unexpected type")
	}
	//: return the recycled or freshly built value.
	return v
}

// Put returns v to the pool for future reuse. Callers MUST NOT touch v after
// Put returns; the pool may hand it to another goroutine immediately.
func (r *Pool[T]) Put(v T) {
	//: hand the value back to sync.Pool — boxing a pointer-sized T is alloc-free.
	r.pool.Put(v)
}
