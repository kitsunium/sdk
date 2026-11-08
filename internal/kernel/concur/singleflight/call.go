package singleflight

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// call is one execution of fn, shared by every caller that named its key while
// it was running.
//
// Every field except base, done and cancel is written by exactly one
// goroutine — the one running fn — and read by the waiters only AFTER
// close(done), which is the happens-before edge that makes the reads
// race-free without a second mutex. refs and retired are the exception: they
// are group bookkeeping and are read and written under the mutex of the shard
// holding the call.
type call[V any] struct {
	// base is the shared context's root: the leader's values, none of its
	// deadline or cancellation (#813). Written once, before the call is
	// published to any other goroutine, and read-only after.
	base detached
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
	// refs counts the callers still waiting. Guarded by the shard's mutex.
	refs int
	// retired records that the call has left the Group's map, so a late
	// abandonment cannot delete a key that now belongs to a different call.
	// Guarded by the shard's mutex.
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

// detached is the root of a shared call's context: the leader's values and
// none of its deadline or cancellation — what context.WithoutCancel(leader)
// returns, kept inside the call record so that it costs no allocation of its
// own (#813).
//
// Only the context.WithCancel built over it is ever handed to fn. That one
// finds Done nil here, so it never watches this root, and answers the context
// package's own lookups itself — Cause, a child looking for its parent, the
// private key context.WithoutCancel answers nil to — so none of them reaches
// this root, and delegating every other Value to the leader's context gives
// exactly WithoutCancel's answers. identity_internal_test.go compares the two
// constructions on every observable, against the code before #813 and after.
//
// Being inside the record, the root keeps the record reachable for as long as
// fn's context is: an fn that hands its context to work outliving the call
// keeps the call's result alive with it, where WithoutCancel kept only the
// leader's context. Nothing a caller reads changes.
type detached struct {
	// parent is the leader's context, read for its values alone.
	parent context.Context
}

// Deadline reports none: the leader's deadline is not the call's.
func (*detached) Deadline() (deadline time.Time, ok bool) {
	//: no deadline, whatever the leader had.
	return time.Time{}, false
}

// Done is nil: nothing but the Group cancels the call, on the context built
// over this one.
func (*detached) Done() <-chan struct{} {
	//: never closed.
	return nil
}

// Err is nil: the root is never cancelled.
func (*detached) Err() error {
	//: never cancelled.
	return nil
}

// Value is the leader's value for key.
func (d *detached) Value(key any) any {
	//: the leader's values, kept.
	return d.parent.Value(key)
}

// String names the context as context.WithoutCancel's does, so a context
// printed in a log reads as it did before #813.
func (d *detached) String() string {
	//: the parent's own name when it has one, as the context package names it.
	if named, ok := d.parent.(interface{ String() string }); ok {
		return named.String() + ".WithoutCancel"
	}
	//: else its type, as the context package does.
	return reflect.TypeOf(d.parent).String() + ".WithoutCancel"
}
