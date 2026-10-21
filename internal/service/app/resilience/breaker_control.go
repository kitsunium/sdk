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
	if b.state == stateOpen && !b.isolated {
		v.OpenUntil = b.openedAt.Add(b.openFor)
	}
	//: a copy: the caller holds nothing of the breaker.
	return v
}

// Isolate opens the breaker until Reset. Calls in flight become stragglers.
func (b *circuitBreaker) Isolate() {
	b.mu.Lock()
	b.isolated = true
	b.generation++
	b.probing = false
	b.moveTo(stateOpen)
	deliver := b.claimDelivery()
	b.mu.Unlock()
	b.deliverIf(deliver)
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
	b.moveTo(stateClosed)
	//: moveTo empties the window on a change; a Reset of a Closed breaker
	//: empties it too.
	if b.window != nil {
		b.window.clear()
	}
	deliver := b.claimDelivery()
	b.mu.Unlock()
	b.deliverIf(deliver)
}

// claimDelivery reports whether the caller must deliver the queued changes:
// there are some, and nobody is delivering them yet. Holds mu.
func (b *circuitBreaker) claimDelivery() bool {
	//: nothing queued, or a goroutine already hands the queue out.
	if b.delivering || b.next == len(b.changes) {
		//: not this caller's to deliver.
		return false
	}
	b.delivering = true
	//: this caller delivers, outside mu.
	return true
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
			b.delivering = false
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
	//: every change was handed out.
	if b.next == len(b.changes) {
		b.changes = b.changes[:0]
		b.next = 0
		b.delivering = false
		//: nothing left.
		return phaseChange{}, false
	}
	change := b.changes[b.next]
	b.next++
	//: the oldest change.
	return change, true
}
