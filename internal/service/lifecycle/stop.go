// Package lifecycle — hosts the shutdown sequence and the per-component
// budget that bounds it.
package lifecycle

import (
	"context"
	"errors"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Stop takes every started component down, in reverse of the order they
// started in.
//
// ctx is read for its VALUES and never for its cancellation. A shutdown is
// normally driven BY a cancelled context — the one that told the process to
// go is the one that was cancelled — so honouring it here would make every
// real shutdown a no-op, and would hand each component's Stop a context that
// is already dead before it has closed anything. Each Stop instead receives a
// context derived with context.WithoutCancel, cancelled only when that
// component's own budget expires.
func (l *lifecycle) Stop(ctx context.Context) error {
	//: Start and Stop are whole operations and never interleave.
	l.opMu.Lock()
	defer l.opMu.Unlock()
	order := l.takeStarted()
	//: idempotent: a Lifecycle that never started, or that already stopped,
	//: has nothing to take down. That is not an error — it is the state a
	//: deferred Stop after a failed Start is always in.
	if len(order) == 0 {
		//: nothing is up.
		l.release()
		//: a no-op shutdown succeeded.
		return nil
	}
	failures := l.stopEach(ctx, order)
	l.release()
	//: errors.Join yields a genuine nil for an empty slice.
	return errors.Join(failures...)
}

// stopEach takes down every component in the order given — which is already
// the reverse of the start order — and collects what failed.
//
// It is the SINGLE unwind path: an ordinary Stop and the cleanup after a
// partial start both come through here, so the two can never drift apart. A
// failure never short-circuits: a component that cannot close cleanly must not
// prevent the ones before it in the order from being asked.
func (l *lifecycle) stopEach(ctx context.Context, order []corelc.ComponentValue) []error {
	failures := make([]error, 0, len(order))
	//: reverse order, one budget each, no short-circuit.
	for _, component := range order {
		err := l.callStop(ctx, component)
		//: the ordinary path.
		if err == nil {
			//: nothing to collect.
			continue
		}
		failures = append(failures, err)
	}
	//: every component was asked, whatever the earlier ones reported.
	return failures
}

// callStop runs one component's Stop under its own budget.
//
// Goroutine lifecycle: exactly one goroutine per call, carrying the
// component's Stop. It ends when that Stop returns — which may be after this
// function has already given up on it, and is why `done` is buffered: an
// abandoned Stop's eventual return must not park a goroutine forever on a
// receiver that has moved on. The engine deliberately does NOT wait for it:
// Go cannot kill a goroutine, and waiting would make the budget a suggestion.
func (l *lifecycle) callStop(parent context.Context, component corelc.ComponentValue) error {
	//: detached from the caller's context — see Stop's doc comment — and
	//: cancelled only by this component's own budget.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	begun := l.clk.Now()
	//: the budget is armed on the INJECTED clock, so a test moves it without
	//: sleeping and production waits on the real one. It is armed BEFORE the
	//: work starts, not after: a budget that only begins once the goroutine
	//: happens to be scheduled is a budget whose start nobody can name.
	timer := l.clk.NewTimer(l.budget)
	//: release it on every exit path.
	defer timer.Stop()
	//: buffered, so an abandoned Stop's eventual return never blocks its
	//: goroutine on a receiver that has already moved on.
	done := make(chan error, 1)
	go func() {
		done <- guard(ctx, component.Name, corelc.PhaseStop.String(), component.Stop)
	}()
	select {
	//: the component returned within its budget.
	case err := <-done:
		//: report whatever it said, verbatim.
		return l.stopped(component.Name, begun, err)
	//: the budget expired.
	case <-timer.C():
		//: announce, stop waiting, and move on.
		return l.abandoned(cancel, component.Name, begun)
	}
}

// stopped reports a component that returned within its budget.
func (l *lifecycle) stopped(name string, begun time.Time, err error) error {
	l.emit(corelc.TransitionValue{
		Name: name, Phase: corelc.PhaseStop, Begun: begun, Err: err,
	})
	//: the ordinary path.
	if err == nil {
		//: clean.
		return nil
	}
	//: the sentinel and the component's own error travel side by side, for
	//: the same reason joinStartFailure gives: origin-wins would otherwise
	//: relabel STOP_FAILED with whatever the component returned.
	return errors.Join(kerrs.Wrap(StopFailed, kerrs.WrapParams{},
		kerrs.String("component", name)), err)
}

// abandoned reports a component whose Stop outlived its budget.
//
// The engine cancels the context it handed that Stop — an ANNOUNCEMENT, the
// one piece of information the component cannot otherwise have — and then
// stops waiting. It does not kill the goroutine, which Go cannot do, and it
// closes nothing on the component's behalf: severing a resource under live
// work is precisely what ADR 0043 removed from the HTTP drain. Every
// component before this one in the reverse order still gets its own full
// budget, which is why the budget is per-component and not shared.
func (l *lifecycle) abandoned(cancel context.CancelFunc, name string, begun time.Time) error {
	//: tell the component its time is up; it decides what that means.
	cancel()
	err := kerrs.Wrap(StopTimeout, kerrs.WrapParams{},
		kerrs.String("component", name), kerrs.String("budget", l.budget.String()))
	l.emit(corelc.TransitionValue{
		Name: name, Phase: corelc.PhaseStop, Begun: begun, TimedOut: true, Err: err,
	})
	//: the shutdown continues with the next component.
	return err
}
