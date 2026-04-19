// Package buffer provides a sync.Pool of []byte for zero-alloc formatting
// in hot logging paths. Consumers acquire a reusable buffer via Get and
// return it via Put so that steady-state allocations tend toward zero.
package buffer

import "sync"

// initialCap is the starting capacity (in bytes) handed out by the pool;
// chosen to comfortably hold a single log line without a re-alloc.
const initialCap int = 1024

// maxRetain bounds the capacity we are willing to keep alive in the pool;
// buffers that grew past this threshold are dropped to prevent the pool
// from retaining unbounded memory after a rare large log record.
const maxRetain int = 64 * 1024

// pool holds the reusable byte buffers; storing *[]byte (rather than []byte)
// avoids boxing the slice header each time Put returns it to the pool.
var pool = sync.Pool{
	New: func() (b any) {
		//: allocate a fresh zero-length buffer and return its address.
		return new(make([]byte, 0, initialCap))
	},
}

// Get borrows a reusable byte buffer with zero length and capacity >= initialCap.
//
// Returns:
//   - *[]byte: a pointer to a zero-length slice ready for append-based writes.
func Get() (b *[]byte) {
	//: fetch any available buffer from the pool; New guarantees a non-nil fallback.
	raw := pool.Get()
	//: comma-ok assertion — defensive even though New always returns *[]byte.
	ptr, ok := raw.(*[]byte)
	//: unexpected pool contents — allocate a fresh buffer rather than panic.
	if !ok {
		//: return a freshly allocated buffer with the standard capacity.
		return new(make([]byte, 0, initialCap))
	}
	//: return the borrowed buffer pointer to the caller.
	return ptr
}

// Put returns a buffer to the pool after use, resetting its length to zero.
// Buffers that grew beyond maxRetain are dropped to keep pool memory bounded.
// A nil argument is a safe no-op so call sites can always defer Put.
//
// Params:
//   - b: pointer to the buffer previously obtained from Get; may be nil.
func Put(b *[]byte) {
	//: short-circuit when the buffer is nil or oversized — both drop the entry.
	if b == nil || cap(*b) > maxRetain {
		//: skip recycling; let GC reclaim any allocation.
		return
	}
	//: reset logical length so the next Get sees an empty buffer.
	*b = (*b)[:0]
	//: hand the buffer back for reuse by a future Get.
	pool.Put(b)
}
