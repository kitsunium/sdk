// Package resilience — what one hedged attempt reports back to the race.
package resilience

import (
	"context"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

// attemptOutcome is how one copy of a hedged Operation ended: the error it
// returned, or the value it panicked with.
//
// A panic travels as a value because it cannot travel any other way. An
// attempt runs on a goroutine of its own, recover() only sees a panic from a
// deferred call of the goroutine that raised it, and a panic that reaches the
// top of ANY goroutine ends the whole process — so it is recovered where it
// happens and handed to hedge.Run, which re-raises it on the caller's goroutine.
type attemptOutcome struct {
	// err is what the copy returned; meaningful only when panicked is false.
	err error
	// raised is the value the copy panicked with, verbatim. It is re-raised
	// unchanged so the caller's recover compares equal to what the Operation
	// panicked with — see NewHedge.
	raised any
	// panicked records that the copy never returned. It is a separate flag
	// rather than a nil test on raised so the question "did it panic" does not
	// depend on what the payload happens to be.
	panicked bool
}

// capture runs op once and records how it ended, turning a panic into an
// outcome instead of letting it unwind off the attempt's goroutine.
//
// It must be called on the goroutine that runs op: recover() reports a panic
// only when called directly by a deferred function of the panicking
// goroutine, which is why this is the one place in the policy that can do it.
func (o *attemptOutcome) capture(ctx context.Context, op coreres.Operation) {
	//: presumed until op returns, so a copy that unwinds instead is caught
	//: whatever its payload: under GODEBUG=panicnil=1 a panic(nil) recovers as
	//: nil, and a nil test on the recovered value would report it as a success.
	o.panicked = true
	defer o.recoverPanic()
	o.err = op(ctx)
	o.panicked = false
}

// recoverPanic stores the value of a panic unwinding through capture.
//
// It is capture's deferred call and nothing else: recover() only stops a panic
// when the deferred function itself calls it, so the call cannot be pushed any
// deeper than this body.
func (o *attemptOutcome) recoverPanic() {
	//: a copy that returned has nothing to recover.
	if o.panicked {
		o.raised = recover()
	}
}
