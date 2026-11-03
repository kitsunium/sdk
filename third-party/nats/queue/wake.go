package queue

import (
	"slices"
	"sync"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// waker is the broker's Waker: a channel closed by this process's own
// publications and hand-backs, and the instants it knows of when a message
// becomes receivable with no further event — a lease it handed out lapsing,
// a retry delay it set ending. Another process's publication is seen at the
// consumer's next poll, as the port says.
type waker struct {
	ready   chan struct{}
	pending []time.Time
	mu      sync.Mutex
}

// newWaker returns a waker with an open signal and no instant.
func newWaker() *waker {
	//: nothing pending.
	return &waker{ready: make(chan struct{})}
}

// signal closes the current channel and remembers at, when a message
// becomes receivable.
func (w *waker) signal(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	woken := w.ready
	w.ready = make(chan struct{})
	close(woken)
	w.pending = append(w.pending, at)
}

// schedule remembers deadline when count leases lapse at it.
func (w *waker) schedule(deadline time.Time, count int) {
	//: no lease handed out.
	if count == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, deadline)
}

// wake is what an idle consumer waits on at now: the signal, and the
// nearest instant still ahead.
func (w *waker) wake(now time.Time) corequeue.WakeValue {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = slices.DeleteFunc(w.pending, func(at time.Time) bool { return at.Before(now) })
	value := corequeue.WakeValue{Signal: w.ready}
	//: nothing known ahead.
	if len(w.pending) == 0 {
		return value
	}
	value.In, value.Scheduled = slices.MinFunc(w.pending, func(a, b time.Time) int { return a.Compare(b) }).Sub(now), true
	//: the nearest.
	return value
}

// Wake returns what an idle consumer should wait on.
func (b *jetStreamBroker) Wake() corequeue.WakeValue {
	//: measured on the broker's clock.
	return b.waker.wake(b.clock.Now())
}
