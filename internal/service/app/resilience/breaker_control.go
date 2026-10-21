package resilience

import coreres "github.com/kitsunium/sdk/internal/core/app/resilience"

// State reports the breaker's phase and the counts it decides on. It moves
// the window to the clock's now, so a count is never older than the window's
// reach, and changes no phase.
func (b *circuitBreaker) State() coreres.BreakerStateValue {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := coreres.BreakerStateValue{
		Phase: b.state, Isolated: b.isolated, Failures: b.failures, Trips: b.trips,
	}
	//: a window reports its own counts, as of now.
	if b.window != nil {
		b.window.advance(b.clk.Now())
		v.Calls, v.Failures, v.SlowCalls = b.window.calls, b.window.failures, b.window.slow
	}
	//: only a cooldown has an end.
	if b.state == coreres.BreakerOpen && !b.isolated {
		v.OpenUntil = b.openedAt.Add(b.openFor)
	}
	//: a copy: the caller holds nothing of the breaker.
	return v
}

// Isolate opens the breaker until Reset. Calls in flight become stragglers.
func (b *circuitBreaker) Isolate() {
	//: the change, under mu; its delivery, outside.
	b.deliverIf(b.isolate())
}

// isolate opens the breaker by hand and reports whether the caller must
// deliver the change it queued. Holds mu.
func (b *circuitBreaker) isolate() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.isolated = true
	b.generation++
	b.probing = false
	b.moveTo(coreres.BreakerOpen)
	//: the change queued, if any, is this caller's to deliver.
	return b.claimDelivery()
}

// Reset closes the breaker and clears its counts and its open backoff. Calls
// in flight become stragglers.
func (b *circuitBreaker) Reset() {
	b.mu.Lock()
	b.isolated = false
	b.generation++
	b.probing = false
	b.failures, b.opens = 0, 0
	b.openFor = b.cooldown
	b.moveTo(coreres.BreakerClosed)
	//: moveTo empties the window on a change; a Reset of a Closed breaker
	//: empties it too.
	if b.window != nil {
		b.window.clear()
	}
	deliver := b.claimDelivery()
	b.mu.Unlock()
	b.deliverIf(deliver)
}

// changeQueue holds the changes of phase OnStateChange has not heard yet:
// changes from next on, in the order they happened. delivering says a
// goroutine is handing them out. It is guarded by the breaker's mu.
type changeQueue struct {
	changes    []phaseChange
	next       int
	delivering bool
}

// push queues one change.
func (q *changeQueue) push(change phaseChange) {
	q.changes = append(q.changes, change)
}

// claim reports whether the caller must deliver the queued changes: there are
// some, and nobody is delivering them yet.
func (q *changeQueue) claim() bool {
	//: nothing queued, or a goroutine already hands the queue out.
	if q.delivering || q.next == len(q.changes) {
		//: not this caller's to deliver.
		return false
	}
	q.delivering = true
	//: this caller delivers, outside mu.
	return true
}

// pop takes the oldest undelivered change, or, when there is none, empties
// the queue — keeping its capacity — and releases the claim.
func (q *changeQueue) pop() (phaseChange, bool) {
	//: every change was handed out.
	if q.next == len(q.changes) {
		q.changes = q.changes[:0]
		q.next = 0
		q.delivering = false
		//: nothing left.
		return phaseChange{}, false
	}
	change := q.changes[q.next]
	q.next++
	//: the oldest change.
	return change, true
}

// claimDelivery reports whether the caller must deliver the queued changes.
// Holds mu.
func (b *circuitBreaker) claimDelivery() bool {
	//: the queue decides; it is empty without a listener.
	return b.queue.claim()
}

// deliverIf delivers the queued changes when the caller claimed them.
func (b *circuitBreaker) deliverIf(claimed bool) {
	//: only the claimant delivers.
	if claimed {
		b.deliver()
	}
}

// deliver hands every queued change to OnStateChange, outside mu, one at a
// time and in the order they were queued, including those queued while it
// runs — by another call or by the listener itself. A listener that panics
// gives the queue back, so the next change claims and delivers what is left.
func (b *circuitBreaker) deliver() {
	drained := false
	//: on a panic, nobody delivers any more until the next change claims it.
	defer func() {
		//: a normal return already released the claim, under mu.
		if !drained {
			b.mu.Lock()
			b.queue.delivering = false
			b.mu.Unlock()
		}
	}()
	//: until the queue is empty.
	for {
		change, ok := b.nextChange()
		//: emptied — the claim is released.
		if !ok {
			drained = true
			//: every change delivered.
			return
		}
		b.onChange(change.from, change.to)
	}
}

// nextChange takes the oldest undelivered change, or, when there is none,
// empties the queue and releases the claim.
func (b *circuitBreaker) nextChange() (phaseChange, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: the queue's oldest, under mu.
	return b.queue.pop()
}
