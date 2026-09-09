// Package lifecycle — hosts the bring-up sequence and the partial-start
// unwind that is its whole reason to exist.
package lifecycle

import (
	"context"
	"errors"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// fixedAggregateParts is how many errors a failed Start's aggregate carries
// before the per-component unwind failures: the SDK's verdict, the
// component's own error, and — at most — the unwind verdict.
const fixedAggregateParts int = 3

// Start brings every registered component up, in registration order.
//
// There is deliberately NO SDK-owned start budget. The caller's context is the
// budget: a start deadline is an operational decision the SDK cannot make, and
// a component that ignores its context could not be bounded by one anyway.
// Cancelling ctx therefore aborts the startup through the components
// themselves — and the unwind that follows does NOT inherit that
// cancellation, because a cleanup driven by the context that just failed is a
// cleanup that does nothing.
func (l *lifecycle) Start(ctx context.Context) error {
	//: Start and Stop are whole operations and never interleave.
	l.opMu.Lock()
	defer l.opMu.Unlock()
	order, err := l.beginStart()
	//: a second Start is refused, not silently joined.
	if err != nil {
		//: propagate LIFECYCLE_RUNNING unchanged.
		return err
	}
	//: bring them up in declaration order, recording each before the next.
	for index, component := range order {
		startErr := l.callStart(ctx, component)
		//: the ordinary path — record it as up and move to the next.
		if startErr == nil {
			l.markStarted(component)
			continue
		}
		//: component `index` failed: unwind everything already up.
		return l.abort(ctx, component.Name, index, startErr)
	}
	//: every component is up.
	return nil
}

// callStart runs one component's Start and reports the transition.
func (l *lifecycle) callStart(ctx context.Context, component corelc.ComponentValue) error {
	begun := l.clk.Now()
	err := guard(ctx, component.Name, corelc.PhaseStart.String(), component.Start)
	l.emit(corelc.TransitionValue{
		Name: component.Name, Phase: corelc.PhaseStart, Begun: begun, Err: err,
	})
	//: verbatim, so the caller's errors.Is keeps working.
	return err
}

// abort cleans up after a failed start and reports both halves.
//
// The failing component is NOT stopped: its Start returned an error, so it
// never handed back a running thing, and calling Stop on a half-constructed
// component is how a double-close panic gets written. A Start that fails owns
// what it acquired, exactly as a Go constructor does — which is the only rule
// under which every other Stop may assume its Start succeeded.
func (l *lifecycle) abort(ctx context.Context, name string, index int, cause error) error {
	//: the SAME reversal and the SAME per-component budget an ordinary Stop
	//: uses. One unwind path, so the cleanup after a partial start cannot
	//: drift from the shutdown it is supposed to be.
	unwound := l.takeStarted()
	failures := l.stopEach(ctx, unwound)
	//: the Lifecycle is back in its not-started state: nothing is up, so a
	//: caller may fix the cause, Add, and Start again.
	l.release()
	//: one aggregate carrying the start failure, the component's own error,
	//: and — only if the cleanup itself broke — a second verdict.
	return joinStartFailure(name, index, len(unwound), cause, failures)
}

// joinStartFailure assembles the aggregate a failed Start returns.
//
// It is an errors.Join and not a wrap chain on purpose. errs.Wrap would hit
// the origin-wins rule (CLAUDE.md rule 6) and inherit the component's own
// code, so a caller could no longer ask "did startup fail?" without knowing
// every code every component might produce. Side by side, both
// errs.HasCode(err, CodeStartFailed) and the caller's own errors.Is answer.
func joinStartFailure(name string, index, unwound int, cause error, failures []error) error {
	parts := make([]error, 0, len(failures)+fixedAggregateParts)
	parts = append(parts, kerrs.Wrap(StartFailed, kerrs.WrapParams{},
		kerrs.String("component", name),
		kerrs.Int("index", index),
		kerrs.Int("unwound", unwound)), cause)
	//: a clean unwind adds nothing — the start failure stands alone.
	if len(failures) > 0 {
		//: a broken teardown is a SECOND defect and is reported as one; it
		//: never replaces the start failure that triggered it.
		parts = append(parts, kerrs.Wrap(UnwindFailed, kerrs.WrapParams{},
			kerrs.String("component", name), kerrs.Int("failed", len(failures))))
		parts = append(parts, failures...)
	}
	//: one aggregate carrying every code a caller might match on.
	return errors.Join(parts...)
}
