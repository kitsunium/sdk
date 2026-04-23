// Package buffer: pool.go provides Recycler[T any], a generic typed pool
// contract backed by sync.Pool. Callers can recycle ANY pointer-sized object
// (event records, attribute slices, scratch structs) without paying the
// boxing cost on Get/Put. The byte-slice pool exposed by buffer.Get /
// buffer.Put remains the idiomatic choice for raw scratch space; Recycler[T]
// complements it for typed objects whose construction is non-trivial.
package buffer

import "sync"

// Recycler describes a typed object pool that hands out values of T from an
// internal cache and accepts them back via Put for future reuse.
// Implementations MUST be safe for concurrent use by multiple goroutines.
type Recycler[T any] interface {
	// Get returns a value of type T, possibly freshly built by the factory
	// supplied at construction time. Callers own the returned value until
	// they hand it back via Put.
	Get() (v T)
	// Put returns a value to the pool for future reuse. Callers MUST NOT
	// retain a reference to v after Put returns; the pool may hand it to
	// another goroutine immediately.
	Put(v T)
}

// objectBucket is the default Recycler implementation backed by sync.Pool.
type objectBucket[T any] struct {
	// p is the underlying sync.Pool that holds the recycled values.
	p sync.Pool
	// new is the factory invoked when p.Get triggers a cache miss; it MUST
	// return a non-zero T because the pool re-caches the value verbatim.
	new func() T
}

// NewRecycler constructs a Recycler[T] backed by sync.Pool. The factory is
// invoked exactly once per cache miss; its result is stored back in the pool
// when Put is called for the corresponding Get.
//
// Params:
//   - newFn: factory called on a cache miss; nil rejects the construction.
//
// Returns:
//   - Recycler[T]: a ready-to-use typed pool, or nil when newFn is nil.
func NewRecycler[T any](newFn func() T) (out Recycler[T]) {
	//: refuse a nil factory — sync.Pool.Get would panic on cache miss otherwise.
	if newFn == nil {
		//: documented contract: caller must supply a factory.
		return nil
	}
	//: build the bucket with a thin closure that boxes the factory output for sync.Pool.
	bucket := &objectBucket[T]{new: newFn}
	bucket.p.New = func() (boxed any) {
		//: invoke the caller's factory and hand its result to sync.Pool as any.
		return newFn()
	}
	//: hand the typed bucket back to the caller as the public interface.
	return bucket
}

// Get borrows a value of type T from the bucket, invoking the factory on a
// cache miss. The returned value MAY be a previously Put value or a freshly
// built one — callers MUST treat it as opaquely owned until they Put it back.
//
// Returns:
//   - T: a value previously Put into the bucket, or freshly created via newFn.
func (b *objectBucket[T]) Get() (v T) {
	//: ask sync.Pool for any cached value; New fires on miss to satisfy the call.
	raw := b.p.Get()
	//: comma-ok defends against the (impossible-by-contract) wrong-type case.
	val, ok := raw.(T)
	//: unexpected pool contents — fall back on a fresh factory invocation.
	if !ok {
		//: callers must never observe a zero T; rebuild rather than propagate it.
		return b.new()
	}
	//: return the recycled (or freshly created) value to the caller.
	return val
}

// Put returns a value to the bucket for future reuse. Callers MUST NOT touch v
// after Put returns; the bucket may hand it to another goroutine immediately.
//
// Params:
//   - v: value previously obtained from Get; the bucket re-caches it verbatim.
func (b *objectBucket[T]) Put(v T) {
	//: hand the value back to sync.Pool — boxing here is unavoidable but cheap.
	b.p.Put(v)
}
