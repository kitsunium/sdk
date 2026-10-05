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
