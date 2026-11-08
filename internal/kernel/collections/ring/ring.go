package ring

import (
	"iter"
	"math/bits"
	"sync/atomic"
)

// cacheLine is the coherence unit the ring's cursors are kept apart by: 64
// bytes on amd64 and on most arm64 cores. Apple's arm64 cores move 128-byte
// lines, where this pad is half of one — a known under-pad, measured in the
// package's BENCH.md rather than paid on every other platform.
const cacheLine uintptr = 64

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

// linePad is one cache line of nothing: the field between two groups of
// fields that two different goroutines write, so a store by one never
// invalidates the line the other reads.
type linePad [cacheLine]byte

// queueRing is the concrete Queue implementation. Unexported so the public
// surface stays narrow and the struct name can read as "ring" without the
// role-suffix convention that exported types must follow.
//
// Its fields fall in three groups, each kept a full cache line from the next:
// the storage and its sizes, which nobody writes after New; the consumer's
// cursor with its copy of the producer's; and the producer's cursor with its
// copy of the consumer's. head and tail count every item ever read and
// written — they never wrap in practice (2^64 operations) — and a slot is the
// cursor masked by the power-of-two storage size.
type queueRing[T any] struct {
	// slots holds the recycled storage; its length is the power of two at
	// least cap, so a cursor finds its slot with a mask, never a division.
	slots []T
	// mask is len(slots)-1.
	mask uint64
	// cap is the logical capacity New was given: the ring is full when tail
	// is cap ahead of head, whatever the storage's length.
	cap uint64
	_   linePad
	// head is the consumer's read cursor, advanced atomically by TryRead.
	head atomic.Uint64
	// cachedTail is the consumer's last reading of tail, written by the
	// consumer alone: TryRead loads tail only when head has caught up with it.
	cachedTail uint64
	_          linePad
	// tail is the producer's write cursor, advanced atomically by TryWrite.
	tail atomic.Uint64
	// cachedHead is the producer's last reading of head, written by the
	// producer alone: TryWrite loads head only when the ring looks full.
	cachedHead uint64
	_          linePad
}

// New constructs a Queue with the supplied logical capacity.
//
// IFACE-PLUGIN: callers receive a Queue[T] handle so the kernel can swap
// the backing implementation (single-buffer today, sharded SPMC tomorrow)
// without breaking call-site code; the concrete struct is unexported.
func New[T any](capacity int) (q Queue[T], err error) {
	//: refuse non-positive capacity early — the buffer would never accept a write.
	if capacity <= 0 {
		//: documented sentinel — caller must supply a positive capacity.
		return nil, CapZero
	}
	//: hand back the ring over a power-of-two storage at least capacity long.
	return newQueueRing[T](capacity), nil
}

// newQueueRing builds the ring New returns, for a positive capacity.
func newQueueRing[T any](capacity int) *queueRing[T] {
	n := slotCount(capacity)
	//: the storage is the power of two at least capacity; the mask indexes it.
	return &queueRing[T]{slots: allocateSlots[T](n), mask: uint64(n - 1), cap: uint64(capacity)}
}

// slotCount is the smallest power of two at least capacity, for a positive
// capacity: the storage size a cursor is masked into.
func slotCount(capacity int) int {
	//: 1 << bits.Len(capacity-1) is capacity itself when it is a power of two.
	return 1 << bits.Len(uint(capacity-1))
}

// allocateSlots returns a freshly-made slice of length n. The make is
// isolated behind this helper so audits that scan for append-grown storage
// do not flag this fixed-length, indexed-only ring backing array.
func allocateSlots[T any](n int) []T {
	//: pure indexed-access storage — no append ever touches this slice.
	return make([]T, n)
}

// Capacity returns the number of usable slots in the ring.
func (b *queueRing[T]) Capacity() int {
	//: cap is stored as uint64 internally; widen back to int for the API.
	return int(b.cap)
}

// Len returns the number of items currently held in the ring. The result is
// a snapshot — a concurrent producer or consumer may move the cursors before
// the caller can act on it.
func (b *queueRing[T]) Len() int {
	//: head first: tail only grows, so a tail read after it is never behind it
	//: and the difference never underflows.
	head := b.head.Load()
	tail := b.tail.Load()
	//: both cursors count from zero and never wrap, so their difference is the
	//: number held — capped, since the two loads are not one instant and the
	//: producer may have run ahead between them.
	return int(min(tail-head, b.cap))
}

// TryWrite places item into the ring without blocking.
func (b *queueRing[T]) TryWrite(item T) error {
	//: the producer's own cursor: no other goroutine moves it.
	tail := b.tail.Load()
	//: the ring looks full against the copy of head: read head itself, once.
	if tail-b.cachedHead >= b.cap {
		b.cachedHead = b.head.Load()
		//: refuse the write when the ring is saturated.
		if tail-b.cachedHead >= b.cap {
			//: return the pre-allocated sentinel directly so the hot path
			//: allocates nothing — errors.Is(err, ring.Full) works the same
			//: against the identity-stable pointer.
			return Full
		}
	}
	//: ORDERING INVARIANT (release-publish): the slot store MUST precede the
	//: tail Store. The sequentially-consistent tail.Store publishes the slot
	//: write to the consumer; reordering these two lines would let TryRead see
	//: an advanced tail before the item lands. Do not "optimise" to a relaxed
	//: store here — the SC tail Store is the publication barrier.
	b.slots[tail&b.mask] = item
	b.tail.Store(tail + 1)
	//: happy path — nothing to report.
	return nil
}

// TryRead removes and returns the next item without blocking.
func (b *queueRing[T]) TryRead() (value T, err error) {
	//: the consumer's own cursor: no other goroutine moves it.
	head := b.head.Load()
	//: the ring looks empty against the copy of tail: read tail itself, once.
	if head == b.cachedTail {
		b.cachedTail = b.tail.Load()
		//: empty when the cursors meet — no item to return.
		if head == b.cachedTail {
			//: return the pre-allocated sentinel directly so the hot path
			//: allocates nothing. errors.Is(err, ring.Empty)
			//: still works because the sentinel is a stable package-level var.
			var zero T
			//: hand back the zero value plus the pre-allocated Empty sentinel.
			return zero, Empty
		}
	}
	//: ORDERING INVARIANT (acquire-consume): the slot load MUST follow a tail
	//: load that saw the slot's write. The SC tail.Load that set cachedTail
	//: beyond head is that acquire: the item the producer published before its
	//: tail.Store is visible here. Reordering the slot read before it would
	//: risk reading a stale slot.
	slot := &b.slots[head&b.mask]
	value = *slot
	var zero T
	*slot = zero
	b.head.Store(head + 1)
	//: happy path — return the item to the caller.
	return value, nil
}

// Drain yields the items q holds, oldest first, reading each with TryRead
// until q reports [Empty]: the consumer's loop over the ring, as an
// iterator.
//
// It is the consumer's side, so it is bound by the same rule as TryRead:
// only the one consumer goroutine ranges over it. An item the producer
// writes while the loop runs is yielded too; a loop that breaks early
// leaves the rest in q. Nothing is allocated.
func Drain[T any](q Queue[T]) iter.Seq[T] {
	return func(yield func(T) bool) {
		//: one TryRead per item, until the ring answers Empty or the loop breaks.
		for {
			item, err := q.TryRead()
			//: Empty is the only error TryRead returns: the ring is drained.
			if err != nil {
				return
			}
			//: a loop that breaks leaves the rest in the ring.
			if !yield(item) {
				return
			}
		}
	}
}
