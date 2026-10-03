//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/concur/recycler .

// Package recycler pools objects for reuse, typed: Get borrows one, Put hands
// it back, and the pool never hands out a value of the wrong type.
//
//	buffers := recycler.NewCappedPool(
//	    func() *bytes.Buffer { return new(bytes.Buffer) },
//	    (*bytes.Buffer).Reset,                            // before it is reused
//	    func(b *bytes.Buffer) int { return b.Cap() },
//	    64<<10,                                           // a bigger one is dropped
//	)
//
//	b := buffers.Get()
//	defer buffers.Put(b)
//
// It is the pool the SDK's logger, codecs and network server recycle their
// buffers through, published as an alias of that kernel package (ADR 0159
// §4).
//
// # When to use it
//
// On a hot path that allocates the same kind of object per call — a buffer per
// request, a scratch slice per record — where the garbage collector's work
// shows in a profile. It is sync.Pool with the type checked by the compiler
// rather than asserted at every Get, which is all [Pool] adds; [CappedPool]
// adds the two things a pool of growable buffers needs, a reset on Put and a
// capacity past which a value is dropped rather than kept.
//
// Like sync.Pool, it is a cache and not an allocator you can count on: a
// pooled value may be collected at any garbage collection, and Get builds a
// new one when the pool is empty.
//
// # Pool a pointer
//
// sync.Pool stores an interface, and only a value that fits in one machine word
// is boxed for free. A *[]byte costs nothing to Put; a []byte, a three-word
// header, allocates on every Put — measured in the kernel package's BENCH.md
// at twice the time and one allocation per call. Pool *T, never T.
//
// # Why a cap, and why it drops before it resets
//
// A buffer that grew to hold one huge request would otherwise stay in the pool
// for good, and every later Get would hand that memory out again. [CappedPool]
// asks capOf on each Put and orphans a value whose capacity is past maxCap —
// without resetting it, because a caller may have handed that value's backing
// bytes to someone else. Reset is not a wipe: a pool recycling secrets zeroes
// them itself before Put.
//
// A nil factory, a nil reset or capacity function, and a non-positive cap
// panic at construction, at the line that made the mistake.
package recycler

import (
	krecycler "github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// Pool is a typed object pool over sync.Pool. [Pool].Get borrows a value,
// building one with the factory when the pool is empty, and [Pool].Put hands
// one back; a caller must not touch a value after Put. It resets nothing — a
// caller resets before Put, or uses [CappedPool].
//
// Safe for concurrent use; always used behind the pointer [NewPool] returns.
type Pool[T any] = krecycler.Pool[T]

// CappedPool is a [Pool] that resets a value on Put, and drops — without
// resetting — a value whose capacity is past its threshold. [CappedPool].Get
// borrows and [CappedPool].Put hands back, as the plain pool's do.
//
// Safe for concurrent use; always used behind the pointer [NewCappedPool]
// returns.
type CappedPool[T any] = krecycler.CappedPool[T]

// NewPool returns a [Pool] whose factory newFn builds a value whenever Get
// finds the pool empty. A nil newFn panics here, rather than at the first
// empty Get far from this call.
func NewPool[T any](newFn func() T) *Pool[T] {
	//: the kernel owns the pool; this facade only forwards.
	return krecycler.NewPool(newFn)
}

// NewCappedPool returns a [CappedPool]: newFn builds a value, resetFn returns
// one to its reusable state before it is pooled again, and capOfFn reports the
// capacity compared with maxCap on each Put. A nil function or a non-positive
// maxCap panics here — a threshold of zero would drop every value and turn the
// pool into an allocator that never recycles.
func NewCappedPool[T any](newFn func() T, resetFn func(T), capOfFn func(T) int, maxCap int) *CappedPool[T] {
	//: the kernel owns the pool; this facade only forwards.
	return krecycler.NewCappedPool(newFn, resetFn, capOfFn, maxCap)
}
