package queue

import "sync"

// idleGate is the slot one idle worker of a Consume holds while it polls an
// empty queue for all of them (issue #1049): before it, every idle worker
// asked the broker on its own — eight workers, eight probes of the same empty
// table or eight scans of the same directories per poll interval.
//
// The holder releases it when it finds work — waking every worker waiting on
// it, each of which looks once — or when it stops. A worker holding work
// never holds the slot.
type idleGate struct {
	// found is closed when the holder finds work, and replaced.
	found chan struct{}
	mu    sync.Mutex
	// held reports that an idle worker polls.
	held bool
}

// newIdleGate returns a free slot.
func newIdleGate() *idleGate {
	//: nobody polls yet.
	return &idleGate{found: make(chan struct{})}
}

// enter takes the slot when it is free — took true — or returns the channel
// its holder closes when it finds work.
func (g *idleGate) enter() (found <-chan struct{}, took bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: held: wait on the holder.
	if g.held {
		return g.found, false
	}
	g.held = true
	//: this worker polls now.
	return nil, true
}

// release frees the slot, and when work was found wakes every worker waiting
// on it.
func (g *idleGate) release(work bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held = false
	//: the waiters look at once.
	if work {
		woken := g.found
		g.found = make(chan struct{})
		close(woken)
	}
}
