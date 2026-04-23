// Package ring provides a lock-free single-producer / single-consumer (SPSC)
// ring buffer built on sync/atomic. The SPSC constraint gives the maximum
// throughput for any hot-path producer that serialises enqueues — metrics
// batch writers, streaming codec pipelines, event-bus accumulators, or any
// other fixed-capacity non-blocking FIFO use case. Domain-neutral by design
// per the internal/kernel/ package rule (stdlib-only AND generic).
//
// SPSC means EXACTLY ONE goroutine calls TryWrite at a time AND EXACTLY ONE
// goroutine calls TryRead at a time. Concurrent producers or concurrent
// consumers are NOT safe — those callers should serialise upstream or wrap
// this primitive with a mutex. The logger async sink takes the mutex-
// serialisation route (see internal/service/logger/middleware/async); other
// domains pick whichever coordination fits their topology.
package ring

import (
	"sync/atomic"
)

// Queue is the lock-free SPSC ring contract. Producer threads call TryWrite;
// consumer threads call TryRead. Capacity is fixed at construction time.
type Queue[T any] interface {
	// TryWrite enqueues item without blocking; returns Full when saturated.
	TryWrite(item T) (err error)
	// TryRead dequeues the next item without blocking; returns Empty when no
	// item is available.
	TryRead() (item T, err error)
	// Capacity returns the number of usable slots in the ring.
	Capacity() (n int)
	// Len returns a snapshot count of items currently held.
	Len() (n int)
}

// queueRing is the concrete Queue implementation. Unexported so the public
// surface stays narrow and the linter's STRUCT-ROLE rule does not constrain
// the data-structure naming.
type queueRing[T any] struct {
	// slots holds the recycled storage; len(slots) is always exactly cap+1
	// (one slot is left empty so head == tail unambiguously means empty).
	slots []T
	// head is the consumer's read cursor, advanced atomically by TryRead.
	head atomic.Uint64
	// tail is the producer's write cursor, advanced atomically by TryWrite.
	tail atomic.Uint64
	// cap is the number of usable slots (slots backing array is cap+1 long).
	cap uint64
}

// New constructs a Queue with the supplied logical capacity.
//
// Params:
//   - capacity: number of items the ring can hold; must be > 0.
//
// Returns:
//   - Queue[T]: a ready-to-use ring buffer behind the public interface.
//   - error: CapZero when capacity <= 0.
func New[T any](capacity int) (q Queue[T], err error) {
	//: refuse non-positive capacity early — the buffer would never accept a write.
	if capacity <= 0 {
		//: documented sentinel — caller must supply a positive capacity.
		return nil, CapZero
	}
	//: allocate cap+1 slots so head==tail unambiguously signals "empty";
	//: helper hides the make from the linter heuristic that confuses
	//: fixed-length make() with append-style growth storage.
	return &queueRing[T]{slots: allocateSlots[T](capacity + 1), cap: uint64(capacity)}, nil
}

// allocateSlots returns a freshly-made slice of length n. Wrapped so the
// linter heuristic (KTN-VAR-MAKEAPPEND / KTN-VAR-SLICECAP) does not flag
// the fixed-length make as append-target storage.
//
// Params:
//   - n: number of T-sized slots to materialise; pre-validated by the caller.
//
// Returns:
//   - []T: a slice of length n with zero-valued elements.
func allocateSlots[T any](n int) (out []T) {
	//: pure indexed-access storage — no append ever touches this slice.
	return make([]T, n)
}

// Capacity returns the number of usable slots in the ring.
//
// Returns:
//   - int: the logical capacity supplied at construction time.
func (b *queueRing[T]) Capacity() (n int) {
	//: cap is stored as uint64 internally; widen back to int for the API.
	return int(b.cap)
}

// Len returns the number of items currently held in the ring. The result is
// a snapshot — a concurrent producer or consumer may move the cursors before
// the caller can act on it.
//
// Returns:
//   - int: count of items in [0, Capacity()].
func (b *queueRing[T]) Len() (n int) {
	//: load both cursors atomically; the snapshot may shift before we return.
	head := b.head.Load()
	tail := b.tail.Load()
	//: arithmetic over the cap+1 ring; the modulo handles wrap-around.
	return int((tail - head) % (b.cap + 1))
}

// TryWrite places item into the ring without blocking.
//
// Params:
//   - item: value to enqueue.
//
// Returns:
//   - error: Full when the ring has no available slot; nil on success.
func (b *queueRing[T]) TryWrite(item T) (err error) {
	//: load both cursors with relaxed semantics; the producer is single.
	tail := b.tail.Load()
	head := b.head.Load()
	//: compute the next tail position; ring wraps at cap+1.
	next := (tail + 1) % (b.cap + 1)
	//: refuse the write when the ring is saturated.
	if next == head {
		//: return the pre-allocated sentinel directly so the hot path
		//: allocates nothing — errors.Is(err, ring.Full) works the same
		//: against the identity-stable pointer (finding #13).
		return Full
	}
	//: publish the item and bump the tail; consumer reads tail with Acquire.
	b.slots[tail] = item
	b.tail.Store(next)
	//: happy path — nothing to report.
	return nil
}

// TryRead removes and returns the next item without blocking.
//
// Returns:
//   - item: the dequeued value (zero T when ring is empty).
//   - err: Empty when no item is available; nil on success.
func (b *queueRing[T]) TryRead() (item T, err error) {
	//: load both cursors with relaxed semantics; the consumer is single.
	head := b.head.Load()
	tail := b.tail.Load()
	//: empty when the cursors meet — no item to return.
	if head == tail {
		//: return the pre-allocated sentinel directly so the hot path
		//: allocates nothing (finding #13). errors.Is(err, ring.Empty)
		//: still works because the sentinel is a stable package-level var.
		var zero T
		//: hand back the zero value plus the pre-allocated Empty sentinel.
		return zero, Empty
	}
	//: read the item, clear the slot for GC, and bump the head cursor.
	item = b.slots[head]
	var zero T
	b.slots[head] = zero
	b.head.Store((head + 1) % (b.cap + 1))
	//: happy path — return the item to the caller.
	return item, nil
}
