// Package buffer provides a pooled *[]byte for zero-alloc formatting in hot
// paths. Consumers acquire a reusable buffer via Get and return it via Put so
// steady-state allocations tend toward zero. It is a thin byte-slice
// specialisation over internal/kernel/recycler.CappedRecycler (ADR 0010): the
// recycling mechanism lives in recycler, the 64-KiB capacity threshold and the
// *[]byte type live here.
package buffer

import "github.com/kitsunium/sdk/internal/kernel/recycler"

// initialCap is the starting capacity (in bytes) handed out by the pool;
// chosen to comfortably hold a single log line without a re-alloc.
const initialCap int = 1024

// maxRetain bounds the capacity we are willing to keep alive in the pool;
// buffers that grew past this threshold are dropped to prevent the pool
// from retaining unbounded memory after a rare large log record.
const maxRetain int = 64 * 1024

// bytePool recycles *[]byte via the kernel CappedRecycler. Storing *[]byte
// (rather than []byte) avoids boxing the slice header into sync.Pool's any
// payload on every Put, which is what keeps the hot path allocation-free.
// reset truncates length to zero; capOf reports cap so the recycler drops any
// buffer grown past maxRetain (discard-before-reset).
var bytePool = recycler.NewCappedRecycler[*[]byte](
	func() *[]byte { return new(make([]byte, 0, initialCap)) },
	func(b *[]byte) { *b = (*b)[:0] },
	func(b *[]byte) int { return cap(*b) },
	maxRetain,
)

// Get borrows a reusable byte buffer with zero length and capacity >= initialCap.
func Get() *[]byte {
	//: delegate to the recycler; its factory guarantees a non-nil *[]byte.
	return bytePool.Get()
}

// Put returns a buffer to the pool after use; the recycler truncates its length
// and drops it when cap exceeds maxRetain. A nil argument is a safe no-op so
// call sites can always defer Put — the generic CappedRecycler cannot nil-check
// a pointer-like T, so the guard lives here.
func Put(b *[]byte) {
	//: nil-safe no-op so callers can defer Put unconditionally.
	if b == nil {
		//: nothing to recycle; let any allocation fall to the GC.
		return
	}
	//: hand the buffer to the recycler (reset + cap-discard happen there).
	bytePool.Put(b)
}
