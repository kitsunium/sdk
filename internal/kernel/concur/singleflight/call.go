// Package singleflight — the in-flight call record shared by every caller of
// one key.
package singleflight

import (
	"context"
	"sync"
)

// call is one execution of fn, shared by every caller that named its key while
// it was running.
//
// Every field except done and cancel is written by exactly one goroutine — the
// one running fn — and read by the waiters only AFTER close(done), which is
// the happens-before edge that makes the reads race-free without a second
// mutex. refs and retired are the exception: they are group bookkeeping and
// are read and written under Group.mu.
type call[V any] struct {
	// done is closed once val/err/panicked are final. Closing rather than
	// sending is what lets N waiters observe one result.
	done chan struct{}
	// publishOnce guards close(done). Only run's defer chain reaches it today,
	// but a second close panics and a panicking publish is the one failure
	// that leaves every waiter parked on a channel forever — so the guard is
	// on the type rather than on a maintainer remembering the invariant.
	publishOnce sync.Once
	// cancel stops the shared context. It is fired when the last caller
	// abandons the call, and again (harmlessly) when fn returns.
	cancel context.CancelFunc
	// refs counts the callers still waiting. Guarded by Group.mu.
	refs int
	// retired records that the call has left the Group's map, so a late
	// abandonment cannot delete a key that now belongs to a different call.
	// Guarded by Group.mu.
	retired bool

	// val and err are fn's outcome.
	val V
	err error
	// panicked is non-nil when fn panicked; every waiter re-raises it.
	panicked *PanicValue
}

// capturePanic is deferred INSIDE the goroutine running fn: recover() only
// reports a panic when called directly by a deferred function of the panicking
// frame, so this cannot be folded into run's own defer chain body.
func (c *call[V]) capturePanic() {
	//: recover returns nil on the normal path, which is the overwhelming case.
	if raised := recover(); raised != nil {
		//: capture the value AND the stack — the stack is the only thing that
		//: still points at the code that actually failed once the panic has
		//: crossed a goroutine boundary.
		c.panicked = newPanicValue(raised)
	}
}

// repanic re-raises fn's panic in the calling goroutine. Called only after
// done is closed.
func (c *call[V]) repanic() {
	//: the normal path — fn returned.
	if c.panicked == nil {
		//: nothing to re-raise.
		return
	}
	//: every waiter panics: N callers asked for a result and none can have
	//: one, so N panics is the honest count, not an amplification bug.
	panic(*c.panicked)
}
