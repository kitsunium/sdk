package resilience

import (
	"context"
	"errors"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
)

// Run rejects fast when Open — and when HalfOpen while its one trial is still
// running —, else runs op and records the outcome. A deterministic error (one
// the classifier rejects) is passed through without touching the state machine;
// a trial that ends that way, or panics, frees the HalfOpen slot for the next
// call.
func (b *circuitBreaker) Run(ctx context.Context, op coreres.Operation) error {
	//: gate on the current state; a closed gate rejects fast.
	adm, ok := b.allow()
	//: Open within its cooldown, isolated, or HalfOpen with its trial in flight.
	if !ok {
		b.deliverIf(adm.deliver)
		//: the call is rejected fast.
		return wrapAs(coreres.CircuitOpen, nil)
	}
	//: the HalfOpen trial holds a slot to free, whatever happens to it.
	if adm.trial {
		//: the rare path, with its deferred release.
		return b.runTrial(ctx, op, adm)
	}
	//: a window times each call on the breaker's clock.
	if b.window != nil {
		//: two clock reads around the operation.
		return b.runTimed(ctx, op, adm)
	}
	//: the consecutive breaker's closed path reads no clock and defers nothing.
	err := op(ctx)
	b.deliverIf(b.record(adm, b.classify(ctx, err), time.Time{}, false))
	//: propagate the operation's own outcome.
	return err
}

// runTimed runs a call admitted while Closed under a window: timed on the
// breaker's clock, its end and whether it was slow handed to record.
func (b *circuitBreaker) runTimed(ctx context.Context, op coreres.Operation, adm admission) error {
	start := b.clk.Now()
	err := op(ctx)
	end := b.clk.Now()
	b.deliverIf(b.record(adm, b.classify(ctx, err), end, end.Sub(start) >= b.window.slowAfter))
	//: propagate the operation's own outcome.
	return err
}

// runTrial runs the HalfOpen trial. A trial that panics must still free the
// slot, or the breaker would stay HalfOpen and reject every call forever, so
// its release is deferred; the change to HalfOpen this call made is heard
// before the trial runs.
func (b *circuitBreaker) runTrial(ctx context.Context, op coreres.Operation, adm admission) error {
	settled := false
	//: settle as neutral unless record already settled it.
	defer b.release(adm, &settled)
	b.deliverIf(adm.deliver)
	var start time.Time
	//: only a window has a slow-call criterion.
	if b.window != nil {
		start = b.clk.Now()
	}
	//: run the guarded operation.
	err := op(ctx)
	end, slow := start, false
	//: the end of the trial, on the same clock.
	if b.window != nil {
		end = b.clk.Now()
		slow = end.Sub(start) >= b.window.slowAfter
	}
	deliver := b.record(adm, b.classify(ctx, err), end, slow)
	settled = true
	b.deliverIf(deliver)
	//: propagate the operation's own outcome.
	return err
}

// classify turns a finished call into a health signal. A deterministic error
// and a caller who left are no signal; a transient error is a failure.
func (b *circuitBreaker) classify(ctx context.Context, err error) outcome {
	//: a nil error is the one success.
	if err == nil {
		//: the dependency answered.
		return outcomeSuccess
	}
	//: the caller cancelled and the operation said so: the caller left, not the
	//: dependency, so a wave of disconnects cannot open a healthy breaker. A
	//: DeadlineExceeded is not this case — a dependency too slow for the
	//: caller's deadline is a slow dependency, and stays a failure.
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		//: neither a success nor a failure; a trial frees its slot.
		return outcomeNeutral
	}
	//: a transient error counts against the dependency.
	if isRetryable(b.retryable, err) {
		//: a dependency failure.
		return outcomeFailure
	}
	//: a deterministic error says nothing.
	return outcomeNeutral
}

// allow reports whether a call may proceed, transitioning Open→HalfOpen once the
// cooldown elapses, and admits at most one HalfOpen trial at a time. An
// isolated breaker admits nothing. Holds mu.
func (b *circuitBreaker) allow() (admission, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: Closed admits the call, and has no change to deliver.
	if b.state == coreres.BreakerClosed {
		//: the hot path: one lock, no clock.
		return admission{generation: b.generation}, true
	}
	//: an Open breaker only admits once its cooldown has elapsed.
	if b.state == coreres.BreakerOpen {
		//: isolated, or still cooling down — reject the call.
		if b.isolated || b.clk.Since(b.openedAt) < b.openFor {
			//: within the Open window.
			return admission{}, false
		}
		//: cooldown elapsed — the next call is the trial.
		b.moveTo(coreres.BreakerHalfOpen)
		b.probing = false
	}
	//: a trial is already probing the dependency: one trial at a time.
	if b.probing {
		//: rejected; a change still queued is delivered by whoever claims it.
		return admission{deliver: b.claimDelivery()}, false
	}
	//: this call is the trial.
	b.probing = true
	//: admitted as the trial of the current generation.
	return admission{generation: b.generation, trial: true, deliver: b.claimDelivery()}, true
}

// release settles a trial its operation abandoned by panicking: record never
// ran, so the trial counts as neutral and the HalfOpen slot is freed. A trial
// record already settled is left alone — the slot may belong to the next one.
func (b *circuitBreaker) release(adm admission, settled *bool) {
	//: record already folded the outcome in.
	if *settled {
		//: nothing left to free.
		return
	}
	//: the abandoned trial carries no health signal.
	b.deliverIf(b.record(adm, outcomeNeutral, time.Time{}, false))
}

// record folds a call outcome into the state machine and reports whether the
// caller must deliver the changes of phase it queued. An outcome from an
// earlier generation is ignored, and a success closes the breaker only from
// Closed or from its HalfOpen trial. end is when the call returned on the
// breaker's clock and slow whether it lasted the window's SlowAfter; both are
// read only with a window. Holds mu.
func (b *circuitBreaker) record(adm admission, res outcome, end time.Time, slow bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: a straggler admitted before the last trip says nothing about now.
	if adm.generation != b.generation {
		//: stale — ignored.
		return false
	}
	//: a trial's end frees the HalfOpen slot, whatever it says.
	if adm.trial {
		//: the next call may probe if this one is neutral.
		b.probing = false
	}
	//: dispatch on the outcome.
	switch res {
	//: a success closes from the trial and clears the count in Closed.
	case outcomeSuccess:
		b.succeed(adm, end, slow)
	//: a failure counts in Closed and re-opens from HalfOpen.
	case outcomeFailure:
		b.fail(end, slow)
	//: a deterministic error leaves the state machine as it was.
	case outcomeNeutral:
	}
	//: a change queued here is this caller's to deliver, unless one already is.
	return b.claimDelivery()
}

// succeed folds one success into the state machine. Holds mu (via record).
func (b *circuitBreaker) succeed(adm admission, now time.Time, slow bool) {
	//: dispatch on the state the success lands in.
	switch b.state {
	//: only the trial may close; a slow trial says the dependency is still slow.
	case coreres.BreakerHalfOpen:
		b.settleTrial(adm, slow)
	//: normal operation: the streak ends, or the window counts the call.
	case coreres.BreakerClosed:
		b.failures = 0
		//: a window may open even on a success: a slow one, or the call that
		//: brings it to MinCalls.
		if b.window != nil && b.window.add(now, false, slow) {
			//: the window's rate is reached.
			b.trip()
		}
	//: an Open breaker of the same generation has nothing to learn.
	case coreres.BreakerOpen:
	}
}

// settleTrial closes the breaker on a successful trial, or opens it again
// when the trial was slow. Holds mu (via record).
func (b *circuitBreaker) settleTrial(adm admission, slow bool) {
	//: the trial is the only current-generation call of a HalfOpen breaker.
	if !adm.trial {
		//: nothing to learn.
		return
	}
	//: a trial at or past SlowAfter fails the trial.
	if slow {
		//: back to Open.
		b.trip()
		return
	}
	b.passed++
	//: more successful trials are asked for: stay HalfOpen, the slot freed
	//: for the next trial.
	if b.passed < b.trials {
		//: not yet recovered.
		return
	}
	//: recovery: closed, counts cleared, the open backoff starts over.
	b.failures, b.opens = 0, 0
	b.moveTo(coreres.BreakerClosed)
}

// fail folds one counted failure into the state machine. Holds mu (via record).
func (b *circuitBreaker) fail(now time.Time, slow bool) {
	//: dispatch on the state the failure lands in.
	switch b.state {
	//: the probe failed — back to Open with a fresh timer.
	case coreres.BreakerHalfOpen:
		b.trip()
	//: a window decides on its rates.
	case coreres.BreakerClosed:
		//: the failure brings the window to its threshold.
		if b.window != nil {
			//: open the circuit when the rate is reached.
			if b.window.add(now, true, slow) {
				b.trip()
			}
			return
		}
		b.failures++
		//: too many consecutive failures opens the circuit.
		if b.failures >= b.threshold {
			//: open the circuit.
			b.trip()
		}
	//: an Open breaker of the same generation has nothing to learn.
	case coreres.BreakerOpen:
	}
}

// trip moves the breaker to Open, stamps the cooldown start, sizes the
// cooldown and starts a new generation, so every call still in flight becomes
// a straggler. Holds mu.
func (b *circuitBreaker) trip() {
	//: enter Open and start the cooldown clock.
	b.openedAt = b.clk.Now()
	b.generation++
	b.probing = false
	b.trips++
	b.opens++
	b.openFor = b.cooldownFor(b.opens)
	b.moveTo(coreres.BreakerOpen)
}

// cooldownFor is how long the n-th opening since the breaker last closed
// lasts: OpenBackoff's curve when one is set, OpenDuration otherwise.
func (b *circuitBreaker) cooldownFor(opens int) time.Duration {
	//: a fixed cooldown.
	if b.backoff == nil {
		//: OpenDuration, every time.
		return b.cooldown
	}
	//: the published curve, at least a nanosecond so the breaker rejects.
	return max(b.backoff.Delay(opens), time.Nanosecond)
}

// moveTo enters phase to, empties the window — a rate read across two phases
// would judge the dependency on calls the last decision already judged — and
// queues the change for OnStateChange when there is one. Holds mu.
func (b *circuitBreaker) moveTo(to breakerState) {
	from := b.state
	b.state = to
	//: the successful trials in a row are counted within one HalfOpen phase.
	b.passed = 0
	//: every change of phase starts the window over.
	if b.window != nil {
		b.window.clear()
	}
	//: a listener is owed every change, in order.
	if b.onChange != nil && from != to {
		b.queue.push(phaseChange{from: from, to: to})
	}
}
