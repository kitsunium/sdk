package ring

import "sync/atomic"

// MultiQueue is the lock-free MPSC ring contract: any number of goroutines
// call TryWrite at once; exactly one calls TryRead. Capacity is fixed at
// construction time, a power of two.
type MultiQueue[T any] interface {
	// TryWrite enqueues item without blocking; returns Full when saturated.
	// Any number of goroutines may call it at once.
	TryWrite(item T) (err error)
	// TryRead dequeues the next item without blocking; returns Empty when no
	// item is available, including while the writer that claimed the next
	// slot has not finished storing into it. One goroutine only.
	TryRead() (item T, err error)
	// Capacity returns the number of usable slots in the ring.
	Capacity() (n int)
	// Len returns a snapshot count of items currently held, a claimed slot
	// still being written included.
	Len() (n int)
}

// multiSlot is one cell of the MPSC ring: the item and the sequence number
// that says whose turn the cell is. A cell at index i holds seq == pos when
// it is free for the writer of position pos, and seq == pos+1 once that
// writer has stored into it, for the reader of position pos.
type multiSlot[T any] struct {
	// seq is the cell's turn, stored with release after the item and loaded
	// with acquire before it.
	seq atomic.Uint64
	// item is the value the writer of the cell's turn stored.
	item T
}

// multiRing is the concrete MultiQueue: Dmitry Vyukov's bounded queue, its
// many-consumer half reduced to one. Writers race for a position on tail with
// a compare-and-swap, then publish the cell through its sequence number; the
// one reader walks head without one. The writers' cursor and the reader's
// are kept a cache line apart.
type multiRing[T any] struct {
	// slots holds the cells; its length is a power of two.
	slots []multiSlot[T]
	// mask is len(slots)-1.
	mask uint64
	_    linePad
	// tail is the next position a writer claims; contended by every writer.
	tail atomic.Uint64
	_    linePad
	// head is the next position the reader reads, written by the reader
	// alone and loaded by Len.
	head atomic.Uint64
	_    linePad
}

// NewMulti constructs a MultiQueue holding the power of two at least
// capacity, which Capacity then reports: CapZero when capacity is not
// positive.
//
// IFACE-PLUGIN: callers receive a MultiQueue[T] handle; the concrete struct
// is unexported. Its four methods are Queue's, so a MultiQueue is also a
// Queue — Drain reads it — but its contract is the wider one.
//
// It is not a drop-in for a Queue behind a mutex. Measured (BENCH.md), one
// producer hands a record over in ~25 ns where the mutex takes ~85, but four
// producers saturating the ring take ~417 ns a record where the mutex takes
// ~235: every writer's claim contends on one cursor while the mutex parks
// its losers. And no writer may read: behind a mutex, a writer that finds
// the ring full can evict the oldest record and store its own in one
// critical section, so the newest record is always admitted; here that
// eviction can only be the reader's, and the writer waits for it or loses
// its own record. Those two are why the SDK's async log sink keeps its Queue
// behind a mutex, with its drop-oldest policy as it is (ADR 0216, #805).
func NewMulti[T any](capacity int) (q MultiQueue[T], err error) {
	//: refuse non-positive capacity early — the buffer would never accept a write.
	if capacity <= 0 {
		//: documented sentinel — caller must supply a positive capacity.
		return nil, CapZero
	}
	//: hand back the ring over the power of two at least capacity.
	return newMultiRing[T](capacity), nil
}

// newMultiRing builds the ring NewMulti returns, for a positive capacity:
// each cell's turn starts at its own index, free for the first lap's writer.
func newMultiRing[T any](capacity int) *multiRing[T] {
	n := slotCount(capacity)
	r := &multiRing[T]{slots: allocateMultiSlots[T](n), mask: uint64(n - 1)}
	//: cell i is the first lap's position i, free until a writer claims it.
	for i := range r.slots {
		r.slots[i].seq.Store(uint64(i))
	}
	//: the ring, every cell free.
	return r
}

// allocateMultiSlots returns the fixed-length, indexed-only cells of an MPSC
// ring, behind a helper as allocateSlots is.
func allocateMultiSlots[T any](n int) []multiSlot[T] {
	//: pure indexed-access storage — no append ever touches this slice.
	return make([]multiSlot[T], n)
}

// Capacity returns the number of usable slots in the ring.
func (r *multiRing[T]) Capacity() int {
	//: every cell is usable: the turn numbers, not a spare cell, tell full from empty.
	return len(r.slots)
}

// Len returns the number of items currently held, a slot claimed and still
// being written included. A snapshot: writers and the reader may move it
// before the caller acts on it.
func (r *multiRing[T]) Len() int {
	//: head first: tail only grows, so a tail read after it is never behind it.
	head := r.head.Load()
	tail := r.tail.Load()
	//: capped, since the two loads are not one instant.
	return int(min(tail-head, uint64(len(r.slots))))
}

// TryWrite claims the next position and stores item into its cell, or
// returns Full when the cell a lap ahead has not been read yet. Any number of
// goroutines may call it at once.
func (r *multiRing[T]) TryWrite(item T) error {
	pos := r.tail.Load()
	//: retry only when another writer took the position first.
	for {
		cell := &r.slots[pos&r.mask]
		seq := cell.seq.Load()
		//: the cell's turn against the position, as a signed distance.
		switch dif := int64(seq - pos); {
		//: the cell is free for this position: claim it.
		case dif == 0:
			//: the claim is the CAS; the loser reloads and tries the next position.
			if r.tail.CompareAndSwap(pos, pos+1) {
				//: ORDERING INVARIANT (release-publish): the item store MUST
				//: precede the seq Store, which hands the cell to the reader.
				cell.item = item
				cell.seq.Store(pos + 1)
				//: happy path — nothing to report.
				return nil
			}
			pos = r.tail.Load()
		//: the cell still holds the previous lap's item: the ring is full.
		case dif < 0:
			//: the pre-allocated sentinel, as the SPSC ring returns it.
			return Full
		//: another writer claimed this position and moved on: catch up.
		default:
			pos = r.tail.Load()
		}
	}
}

// TryRead removes and returns the next item without blocking, or Empty when
// the next cell has not been published — nothing written, or a writer between
// its claim and its store. One goroutine only.
func (r *multiRing[T]) TryRead() (value T, err error) {
	//: the reader's own cursor: no other goroutine moves it.
	pos := r.head.Load()
	cell := &r.slots[pos&r.mask]
	//: ORDERING INVARIANT (acquire-consume): the seq load MUST precede the
	//: item load; seq == pos+1 is the writer's release of the item.
	if cell.seq.Load() != pos+1 {
		var zero T
		//: hand back the zero value plus the pre-allocated Empty sentinel.
		return zero, Empty
	}
	value = cell.item
	var zero T
	cell.item = zero
	//: free the cell for the writer one lap ahead, then move past it.
	cell.seq.Store(pos + r.mask + 1)
	r.head.Store(pos + 1)
	//: happy path — return the item to the caller.
	return value, nil
}
