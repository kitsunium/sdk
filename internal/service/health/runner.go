// Package health — hosts the execution of one check: its budget, its panic
// recovery, and the wrapping that decides what a stranger reading the probe
// body is allowed to learn.
package health

import (
	"context"
	"fmt"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// evaluate answers one check: from the cache if it holds a current success,
// otherwise by waiting out one run under this check's own budget.
//
// # Goroutine lifetime
//
// A cache hit launches nothing. On a miss, [entry.claim] hands exactly one
// caller `mine`, and that caller spawns ONE goroutine running [health.perform]
// — every other probe joins the same run. Its lifetime is the check body's,
// which the SDK does not bound: it ends when the body returns, and nothing
// here ends it earlier, because Go cannot kill a goroutine.
//
// What CAN be stopped is the waiting. When the budget fires, [health.abandoned]
// cancels the run's context — an announcement the body may honour — and this
// function returns without the goroutine. The run stays registered on the
// entry, so the next probe joins it rather than spawning a second: a wedged
// dependency costs one goroutine for the whole outage, not one per poll.
//
// The goroutine's only shared state is the run it was given: it writes
// `result` before closing `done`, and every waiter reads `result` only after
// `done` closes.
//
// The caller can stop waiting too. Its context is its own — a lifecycle
// startup interrupted by a signal, an HTTP client that hung up — and a waiter
// that ignored it sat out the whole budget for an answer nobody would read.
// That is [health.departed], and unlike the budget it cancels nothing.
func (h *health) evaluate(ctx context.Context, e *entry) corehealth.ResultValue {
	//: a replayable success costs nothing and starts nothing.
	if replay, ok := e.fresh(h.clk.Now()); ok {
		//: dated, and marked as dated.
		return replay
	}
	run, mine := e.claim(ctx)
	//: exactly one caller starts the body; the rest wait on the same run.
	if mine {
		go h.perform(e, run)
	}
	armed := h.clk.Now()
	//: the budget is armed on the INJECTED clock, so a test moves it without
	//: sleeping and production waits on the real one.
	timer := h.clk.NewTimer(e.budget)
	//: release it on every exit path.
	defer timer.Stop()
	select {
	//: the check answered within its budget.
	case <-run.done:
		//: whatever it said, verbatim.
		return run.result
	//: the budget expired.
	case <-timer.C():
		//: announce, stop waiting, and report.
		return h.abandoned(e, run)
	//: the caller stopped waiting first. A nil Done — context.Background —
	//: never selects, so a caller with no deadline waits exactly as before.
	case <-ctx.Done():
		//: stop waiting, and leave the shared run alone.
		return h.departed(ctx, e, h.clk.Since(armed))
	}
}

// perform runs one check body and publishes its result to everyone waiting.
func (h *health) perform(e *entry, run *inflight) {
	begun := h.clk.Now()
	//: the run's budget is its own, and expires whether or not anyone is left
	//: waiting for it.
	release := h.boundRun(e, run)
	err := h.invoke(e, run)
	release()
	result := corehealth.ResultValue{
		Name: e.name, Status: e.verdict(err), At: h.clk.Now(),
		Took: h.clk.Since(begun), Err: err,
	}
	//: published BEFORE the entry is released, so a probe arriving in between
	//: joins a finished run and gets its answer at once.
	run.result = result
	close(run.done)
	//: a startup check that just passed shrinks the set the phase reads.
	if e.finish(result) {
		h.startupPassed()
	}
}

// boundRun arms the budget the RUN owns, and returns the release its caller
// runs when the body returns.
//
// The budget used to live only on the waiting side, so the run's context was
// cancelled by whichever probe sat out the whole budget — and a run every
// caller had left was cancelled by nobody. That is the ordinary shape of a
// polled endpoint behind a proxy with its own, shorter timeout: each probe
// departs early (health.departed, which deliberately cancels nothing, since
// one caller's context is not the shared run's), the run keeps the next probe
// from starting a second body, and the body itself is never told its time is
// up. A check that honours its context then holds its dependency's connection
// for the entire outage while every probe reports a timeout.
//
// The announcement is the same one [health.abandoned] makes and means the same
// thing: the SDK cannot kill a goroutine, so cancelling the context is the
// only thing it can do, and the body decides what that means.
//
// # Goroutine lifetime
//
// One goroutine per run, and it does NOT last the outage: it ends at the
// budget — having cancelled — or at the release, whichever comes first. So a
// wedged dependency still costs one goroutine for the whole outage (the body),
// plus this one for the length of one budget.
func (h *health) boundRun(e *entry, run *inflight) (release func()) {
	//: armed on the INJECTED clock, like every other budget here.
	timer := h.clk.NewTimer(e.budget)
	released := make(chan struct{})
	go func() {
		defer timer.Stop()
		select {
		//: the body had its budget and did not answer.
		case <-timer.C():
			//: tell it so; a liveness body has no context and hears nothing.
			run.cancel()
		//: the body answered first.
		case <-released:
			//: nothing to announce.
		}
	}()
	return func() { close(released) }
}

// invoke calls the check body with its panic recovered, and wraps a failure so
// that what reaches a probe body is decided here and nowhere else.
//
// A panic escaping a check would take the whole process down over a health
// question — the endpoint becoming the outage it was watching for.
func (h *health) invoke(e *entry, run *inflight) (err error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: leave err as the check returned it.
			return
		}
		//: the recovered value travels as a FIELD, never as the wrap origin.
		//: A panic string routinely carries an address or a query, and as a
		//: cause it would become the Public a stranger reads.
		err = kerrs.Wrap(corehealth.CheckPanicked, kerrs.WrapParams{},
			kerrs.String("check", e.name), kerrs.String("probe", e.probe.String()),
			kerrs.String("panic", fmt.Sprint(value)))
	}()
	//: wrap after the body returns, so the recover above sees the raw panic.
	return h.wrapFailure(e, e.call(run))
}

// call dispatches to whichever of the two bodies this entry carries.
func (e *entry) call(run *inflight) error {
	//: a liveness body takes no context — that is the domain's central rule
	//: made structural, not a special case (core/health.SelfCheck).
	if e.runSelf != nil {
		//: process-local evidence, nothing to cancel.
		return e.runSelf()
	}
	//: startup and readiness get the run's detached, cancellable context.
	return e.run(run.ctx)
}

// verdict turns an error into this check's status, applying criticality.
func (e *entry) verdict(err error) corehealth.Status {
	//: the ordinary path.
	if err == nil {
		//: passed.
		return corehealth.StatusHealthy
	}
	//: a non-critical failure still serves; that is the whole meaning of the
	//: flag, and the reason Degraded is a serving state.
	if e.nonCritical {
		//: serving, with a caveat.
		return corehealth.StatusDegraded
	}
	//: not serving.
	return corehealth.StatusUnhealthy
}

// wrapFailure gives a check's error an SDK identity WITHOUT overwriting one it
// already has.
//
// This is the whole public/private mechanism doing the work. Origin-wins means
// a caller's own *errs.Error keeps its Code, Reason, Public and Private, so a
// wire-safe message they wrote is what a stranger reads; a plain error — the
// driver's `dial tcp 10.0.3.14:5432: connect: connection refused` — becomes
// [CheckFailed] instead, and its text survives only in the Err a handler never
// renders and Config.OnReport does.
func (h *health) wrapFailure(e *entry, err error) error {
	//: the ordinary path.
	if err == nil {
		//: nothing to label.
		return nil
	}
	//: read from the sentinel rather than repeating its literals, so the
	//: fallback identity cannot drift from the sentinel it names.
	return kerrs.Wrap(err, kerrs.WrapParams{
		Code:    CheckFailed.Code(),
		Reason:  CheckFailed.Reason(),
		Public:  CheckFailed.Public(),
		Private: CheckFailed.Private(),
	}, kerrs.String("check", e.name), kerrs.String("probe", e.probe.String()))
}

// abandoned reports a check whose answer did not arrive within its budget.
//
// The registry cancels the run's context — an ANNOUNCEMENT, the one piece of
// information the check cannot otherwise have — and stops waiting. It does not
// kill the goroutine, which Go cannot do, and it closes nothing the check
// holds. The run stays outstanding, so the NEXT probe joins it rather than
// starting a second one: a wedged dependency costs one goroutine for the
// duration of the outage, not one per poll.
func (h *health) abandoned(e *entry, run *inflight) corehealth.ResultValue {
	//: tell the body its time is up; it decides what that means. A liveness
	//: body has no context and hears nothing, which its own doc comment says.
	run.cancel()
	err := kerrs.Wrap(CheckTimeout, kerrs.WrapParams{},
		kerrs.String("check", e.name), kerrs.String("probe", e.probe.String()),
		kerrs.String("budget", e.budget.String()))
	//: a timeout is a FAILURE, not an unknown — see the CheckTimeout sentinel
	//: — so it goes through the same criticality rule every other failure does.
	return corehealth.ResultValue{
		Name: e.name, Status: e.verdict(err), TimedOut: true,
		At: h.clk.Now(), Took: e.budget, Err: err,
	}
}

// departed reports a check whose CALLER stopped waiting before it answered:
// the probe's context ended before the check returned and before its budget.
//
// It is abandoned's mirror, and the difference is the whole point — the run is
// NOT cancelled. That context belonged to one caller, while the run belongs to
// every probe waiting on it, which is why entry.claim detached it from the
// context that started it. So this probe stops waiting, the run keeps going,
// the next probe JOINS it rather than starting a second body, and that probe's
// own budget is what announces a timeout. A wedged dependency still costs one
// goroutine for the whole outage.
//
// The result is still a failure, and still CheckTimeout with TimedOut set: the
// check said nothing within the time this probe had. What changes is whose
// time ran out, and the chain says so — the caller's context error is the
// cause, so errors.Is(err, context.Canceled) tells a departure apart from an
// expired budget, and Took is how long this probe actually waited.
func (h *health) departed(ctx context.Context, e *entry, waited time.Duration) corehealth.ResultValue {
	//: read from the sentinel so the identity cannot drift from it.
	err := kerrs.Wrap(ctx.Err(), kerrs.WrapParams{
		Code:     CheckTimeout.Code(),
		Reason:   CheckTimeout.Reason(),
		Public:   CheckTimeout.Public(),
		Private:  "service/health: the caller's context ended before the check answered; its run was left running for the next probe",
		ExitCode: CheckTimeout.ExitCode(),
	}, kerrs.String("check", e.name), kerrs.String("probe", e.probe.String()))
	//: a failure like any other, through the same criticality rule.
	return corehealth.ResultValue{
		Name: e.name, Status: e.verdict(err), TimedOut: true,
		At: h.clk.Now(), Took: waited, Err: err,
	}
}
