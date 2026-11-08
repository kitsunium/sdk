package signal

import (
	"context"
	"errors"
	"sync"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// receivedSuffix closes a ReceivedError's message.
const receivedSuffix string = " received"

// ReceivedError is the cause a NotifyContext context is cancelled with: the
// signal that arrived, as an error. It is comparable, so context.Cause(ctx)
// == ReceivedError{Signal: s} holds, and CauseSignal reads it back.
type ReceivedError struct {
	// Signal is the signal that arrived.
	Signal coreproc.Signal
}

// Error names the signal that cancelled the context, e.g. "SIGTERM received".
func (c ReceivedError) Error() string {
	//: "SIGTERM received".
	return c.Signal.String() + receivedSuffix
}

// CauseSignal reports the signal a NotifyContext context was cancelled by, and
// false when it was cancelled otherwise or not at all.
func CauseSignal(ctx context.Context) (sig coreproc.Signal, ok bool) {
	cause, found := errors.AsType[ReceivedError](context.Cause(ctx))
	//: the signal, or the honest absence of one.
	return cause.Signal, found
}

// NotifyContext returns a copy of parent that is cancelled when one of sigs
// arrives, with a ReceivedError naming it as context.Cause — os/signal's
// NotifyContext for the typed Signal, with Notify's guarantee that the
// subscription is live before it returns. stop unsubscribes, cancels the
// context with no signal cause, and returns once the bridge goroutine has
// ended; it is idempotent.
func NotifyContext(parent context.Context, sigs ...coreproc.Signal) (ctx context.Context, stop func()) {
	ctx, cancel := context.WithCancelCause(parent)
	received, unsubscribe := Notify(sigs...)
	done := make(chan struct{})
	go cancelOnSignal(ctx, cancel, received, done)
	var once sync.Once
	stop = func() {
		//: once: unsubscribe, cancel without a signal, and join the bridge.
		once.Do(func() {
			unsubscribe()
			cancel(nil)
			<-done
		})
	}
	//: the context, and the teardown that leaves no goroutine behind.
	return ctx, stop
}

// cancelOnSignal cancels ctx with the first signal received, or ends when ctx
// does or the subscription closes.
func cancelOnSignal(ctx context.Context, cancel context.CancelCauseFunc, received <-chan coreproc.Signal, done chan<- struct{}) {
	defer close(done)
	select {
	//: the first signal is the cause.
	case sig, ok := <-received:
		//: a closed subscription is a stop, not a signal.
		if ok {
			cancel(ReceivedError{Signal: sig})
		}
	//: cancelled by the parent or by stop.
	case <-ctx.Done():
	}
}

// EscalationValue is the policy Escalate applies to repeated signals: what to
// do at a first delivery, at a repeat, and how long a repeat counts as one.
type EscalationValue struct {
	// OnFirst runs at a delivery that opens a window; nil runs nothing.
	OnFirst func(coreproc.Signal)
	// OnRepeat runs once, at a delivery inside the window, and ends the relay;
	// nil runs nothing.
	OnRepeat func(coreproc.Signal)
	// Window is how long after a first delivery a second one escalates. Zero
	// is a window that never closes: any later delivery escalates.
	Window time.Duration
}

// Escalate applies policy to the deliveries of sigs until ctx ends or stop is
// called: a delivery runs OnFirst and opens a window of policy.Window on clk;
// a second delivery of any of sigs inside it runs OnRepeat once, and the relay
// ends. A window that passes with no repeat closes, and the next delivery is a
// first again. A zero Window never closes — any later delivery escalates (ADR
// 0031). It is the "first signal: drain with grace; second: stop at once" the
// framework and every CLI convention apply. The hooks run on the relay's
// goroutine and must not block; either may be nil. On Windows the deliveries
// are the console events the runtime maps to os.Interrupt. stop is
// idempotent and returns once the relay has ended.
func Escalate(ctx context.Context, clk clock.Waiter, policy EscalationValue, sigs ...coreproc.Signal) (stop func()) {
	received, unsubscribe := Notify(sigs...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		escalate(ctx, clk, policy, received)
	}()
	var once sync.Once
	//: once: unsubscribe — which closes received and ends the relay — and join it.
	return func() {
		once.Do(func() {
			unsubscribe()
			<-done
		})
	}
}

// escalate is Escalate's relay over a source of signals: it runs until ctx
// ends, the source closes, or OnRepeat has run.
func escalate(ctx context.Context, clk clock.Waiter, policy EscalationValue, received <-chan coreproc.Signal) {
	var window clock.Timer
	var windowEnds <-chan time.Time
	defer func() {
		//: an armed window is released with the relay.
		if window != nil {
			window.Stop()
		}
	}()
	for {
		select {
		//: stopped by the caller.
		case <-ctx.Done():
			return
		//: the window passed with no repeat: the next delivery is a first again.
		case <-windowEnds:
			windowEnds = nil
		//: a delivery.
		case sig, ok := <-received:
			//: the subscription ended.
			if !ok {
				return
			}
			//: a window that ran out while this delivery was in flight is closed:
			//: the timer, not the order the select drew, decides.
			windowEnds = closeIfDue(windowEnds)
			//: inside the window, or any later one with a zero window.
			if windowEnds != nil {
				run(policy.OnRepeat, sig)
				return
			}
			run(policy.OnFirst, sig)
			window, windowEnds = arm(clk, window, policy.Window)
		}
	}
}

// arm opens the escalation window: a timer of window on clk, or, for a zero
// window, a channel that never fires — any later delivery escalates.
func arm(clk clock.Waiter, timer clock.Timer, window time.Duration) (clock.Timer, <-chan time.Time) {
	//: zero: the window never closes (ADR 0031: a zero is the widest policy).
	if window <= 0 {
		//: never fires, but not nil: the next delivery is a repeat.
		return timer, make(chan time.Time)
	}
	//: the first window of the relay.
	if timer == nil {
		timer = clk.NewTimer(window)
		return timer, timer.C()
	}
	timer.Reset(window)
	//: the window, re-armed.
	return timer, timer.C()
}

// closeIfDue returns nil when the window's timer has already fired, and the
// window unchanged otherwise.
func closeIfDue(windowEnds <-chan time.Time) <-chan time.Time {
	select {
	//: fired: the window is over.
	case <-windowEnds:
		return nil
	//: still open, or no window at all.
	default:
		return windowEnds
	}
}

// run calls fn with sig, when there is a fn.
func run(fn func(coreproc.Signal), sig coreproc.Signal) {
	//: a nil hook is allowed.
	if fn != nil {
		fn(sig)
	}
}
