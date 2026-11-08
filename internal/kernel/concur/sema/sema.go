package sema

import (
	"context"
	"sync"
)

// Weighted is a weighted, context-aware, first-in first-out semaphore: a
// capacity of units, Acquire takes n of them — waiting, within its context,
// behind every Acquire that came first —, and Release gives them back. The
// zero value is NOT usable; build it with NewWeighted.
type Weighted struct {
	// mu guards held and the waiters.
	mu sync.Mutex
	// capacity is the number of units the semaphore holds, at least 1.
	capacity int64
	// held is the number of units acquired and not yet released.
	held int64
	// head and tail are the waiters, oldest first: an intrusive list, so a
	// waiter that leaves is unlinked where it stands.
	head, tail *waiter
}

// waiter is one Acquire waiting for its units: the weight it asked for, the
// channel that receives the grant, and its neighbours in the queue.
type waiter struct {
	// n is the weight asked for.
	n int64
	// granted receives one token, under mu, when the units are the waiter's:
	// one slot, so the grant never blocks, and a waiter that left finds it
	// there with a non-blocking receive.
	granted chan struct{}
	// prev and next link the waiter into the queue.
	prev, next *waiter
}

// NewWeighted builds a Weighted holding capacity units; a capacity below 1 is
// clamped to 1, a semaphore that admits nothing being no semaphore.
func NewWeighted(capacity int64) *Weighted {
	//: a semaphore that admits nothing would park every caller forever.
	return &Weighted{capacity: max(capacity, 1)}
}

// Acquire takes n units, waiting until they are free and every earlier
// waiter has been served: a large request is never overtaken by smaller ones
// behind it. It returns WeightTooLarge at once when n exceeds the capacity —
// such a request could never be served —, and ctx's error, holding nothing,
// when ctx ends first; a ctx already done fails even when the units are free.
// An n of zero is served at once; a negative n panics.
func (s *Weighted) Acquire(ctx context.Context, n int64) error {
	//: a weight below zero would grow the capacity: a programmer's fault.
	if n < 0 {
		panic("internal/kernel/concur/sema: Acquire of a negative weight")
	}
	//: never parked forever on a request nothing could ever serve.
	if n > s.capacity {
		return WeightTooLarge
	}
	done := ctx.Done()
	s.mu.Lock()
	//: a ctx that ended before the call is honoured, free units or not.
	select {
	case <-done:
		s.mu.Unlock()
		//: nothing taken.
		return ctx.Err()
	default:
	}
	//: free units and nobody ahead: taken without waiting.
	if s.head == nil && s.capacity-s.held >= n {
		s.held += n
		s.mu.Unlock()
		//: acquired.
		return nil
	}
	w := &waiter{n: n, granted: make(chan struct{}, 1)}
	s.push(w)
	s.mu.Unlock()
	select {
	case <-w.granted:
		//: the units are ours.
		return nil
	case <-done:
		//: left the queue, or gave back what was granted meanwhile.
		return s.leave(w, ctx.Err())
	}
}

// leave withdraws w after its context ended: unlinked if it still waits, its
// units given back if they were granted in the meantime. Either way the
// waiters behind it may now fit, so they are served.
func (s *Weighted) leave(w *waiter, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-w.granted:
		//: granted as ctx ended: given back, so the caller holds nothing.
		s.held -= w.n
	default:
		s.unlink(w)
	}
	s.grant()
	//: the context's verdict.
	return cause
}

// TryAcquire takes n units without waiting and reports whether it did: false
// when they are not free, when an Acquire is already waiting — it would be
// overtaken —, or when n exceeds the capacity. A negative n panics.
func (s *Weighted) TryAcquire(n int64) bool {
	//: a weight below zero would grow the capacity: a programmer's fault.
	if n < 0 {
		panic("internal/kernel/concur/sema: TryAcquire of a negative weight")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//: free units and nobody ahead.
	if s.head == nil && s.capacity-s.held >= n {
		s.held += n
		//: acquired.
		return true
	}
	//: not without waiting.
	return false
}

// Release gives back n units and serves the waiters they let through, in
// order. Releasing more than is held, or a negative n, panics: a programmer's
// fault, which a silent correction would turn into an admission bound that no
// longer bounds.
func (s *Weighted) Release(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the books would go negative: the caller released what it never held.
	if n < 0 || n > s.held {
		panic("internal/kernel/concur/sema: Release of more units than are held")
	}
	s.held -= n
	s.grant()
}

// grant serves the waiters from the oldest while the oldest fits, and stops
// at the first that does not: FIFO, so a large request at the head is not
// starved by the small ones behind it. The caller holds mu.
func (s *Weighted) grant() {
	//: oldest first, until one does not fit.
	for w := s.head; w != nil && s.capacity-s.held >= w.n; w = s.head {
		s.held += w.n
		s.unlink(w)
		//: the one token of the one slot: never blocks.
		w.granted <- struct{}{}
	}
}

// push appends w to the queue. The caller holds mu.
func (s *Weighted) push(w *waiter) {
	w.prev = s.tail
	//: an empty queue: w is its head as well.
	if s.tail == nil {
		s.head = w
	} else {
		s.tail.next = w
	}
	s.tail = w
}

// unlink removes w from the queue wherever it stands. The caller holds mu.
func (s *Weighted) unlink(w *waiter) {
	//: w's predecessor, or the head, now points past it.
	if w.prev == nil {
		s.head = w.next
	} else {
		w.prev.next = w.next
	}
	//: w's successor, or the tail, now points before it.
	if w.next == nil {
		s.tail = w.prev
	} else {
		w.next.prev = w.prev
	}
	w.prev, w.next = nil, nil
}
