package resilience

import (
	"context"
	"sync"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// defaultThreshold is the consecutive-failure count that opens the breaker when
// none is configured.
const defaultThreshold int = 5

// defaultOpenDuration is the cooldown an Open breaker serves when none is
// configured. A zero cooldown lets the very next call re-probe the downstream,
// which is a breaker that rejects nothing — the caller believes it is protected
// and is not (ADR 0031). Thirty seconds is the same order of arbitrary as the
// five-failure defaultThreshold above and is the industry-conventional range.
const defaultOpenDuration time.Duration = 30 * time.Second

// circuitBreaker trips Open after FailureThreshold consecutive failures, rejects
// for OpenDuration, then half-opens for ONE trial at a time.
//
// generation counts the trips. A call captures it on admission and its outcome
// is folded in only while it is still current: a call admitted before the
// breaker last opened is a straggler, and its result describes a dependency the
// breaker has already judged — its success must not close an Open breaker and
// its failure must not restart a cooldown it did not cause.
type circuitBreaker struct {
	mu         sync.Mutex
	clk        clock.Clock
	retryable  func(error) bool
	threshold  int
	openFor    time.Duration
	state      breakerState
	failures   int
	openedAt   time.Time
	generation uint64
	probing    bool
}

// admission is what allow hands Run: the generation the call was admitted in
// and whether it is the HalfOpen trial, the one call that may close the breaker.
type admission struct {
	generation uint64
	trial      bool
}

// outcome is what a finished call tells the state machine.
type outcome int

const (
	// outcomeNeutral is a deterministic error or an abandoned call: no signal.
	outcomeNeutral outcome = iota
	// outcomeSuccess is a call that returned nil.
	outcomeSuccess
	// outcomeFailure is a call whose error the classifier counts.
	outcomeFailure
)

// NewCircuitBreaker returns a Runner guarding op with a Closed→Open→HalfOpen
// breaker. Open calls return CircuitOpen until OpenDuration elapses; a
// non-positive OpenDuration clamps to defaultOpenDuration, because a breaker
// whose cooldown is zero admits the next call immediately and therefore
// protects nothing (ADR 0031). An error rejected by cfg.Retryable is returned
// verbatim and left out of the state machine entirely, so deterministic
// failures cannot trip the breaker. Once the cooldown elapses ONE HalfOpen
// trial runs at a time and every other call returns CircuitOpen until it
// settles. The outcome of a call admitted before the breaker last opened
// is ignored: its success cannot close the breaker and its failure cannot
// restart the cooldown.
func NewCircuitBreaker(cfg BreakerConfig) coreres.Runner {
	//: default the clock to the system source.
	clk := cfg.Clock
	//: a nil clock falls back to the system source.
	if clk == nil {
		//: production default.
		clk = clock.System
	}
	//: a non-positive threshold falls back to the default.
	threshold := cfg.FailureThreshold
	//: clamp a non-positive threshold to the 5-failure default.
	if threshold < 1 {
		//: standard 5-failure trip.
		threshold = defaultThreshold
	}
	//: a non-positive cooldown falls back to the default.
	openFor := cfg.OpenDuration
	//: clamp it like every sibling knob — a zero cooldown is not a breaker.
	if openFor <= 0 {
		//: standard 30-second cooldown.
		openFor = defaultOpenDuration
	}
	//: a fresh breaker starts Closed.
	return &circuitBreaker{clk: clk, retryable: cfg.Retryable, threshold: threshold, openFor: openFor}
}

// Run rejects fast when Open — and when HalfOpen while its one trial is still
// running —, else runs op and records the outcome. A deterministic error (one
// the classifier rejects) is passed through without touching the state machine;
// a trial that ends that way, or panics, frees the HalfOpen slot for the next
// call.
func (b *circuitBreaker) Run(ctx context.Context, op coreres.Operation) error {
	//: gate on the current state; a closed gate rejects fast.
	adm, ok := b.allow()
	//: Open within its cooldown, or HalfOpen with its trial in flight.
	if !ok {
		//: the call is rejected fast.
		return wrapAs(coreres.CircuitOpen, nil)
	}
	//: a trial that panics must still free the slot, or the breaker would
	//: stay HalfOpen and reject every call forever.
	settled := false
	//: only the trial holds a slot to free.
	if adm.trial {
		//: settle as neutral unless record already settled it.
		defer b.release(adm, &settled)
	}
	//: run the guarded operation.
	err := op(ctx)
	//: classify the outcome; a deterministic error is no health signal.
	res := outcomeSuccess
	//: an error the classifier counts is a failure, any other is neutral.
	if err != nil {
		//: the default: a deterministic error says nothing.
		res = outcomeNeutral
		//: a transient error counts against the dependency.
		if isRetryable(b.retryable, err) {
			//: a dependency failure.
			res = outcomeFailure
		}
	}
	//: fold the health signal into the state machine.
	b.record(adm, res)
	settled = true
	//: propagate the operation's own outcome.
	return err
}

// allow reports whether a call may proceed, transitioning Open→HalfOpen once the
// cooldown elapses, and admits at most one HalfOpen trial at a time. Holds mu.
func (b *circuitBreaker) allow() (admission, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: an Open breaker only admits once its cooldown has elapsed.
	if b.state == stateOpen {
		//: still cooling down — reject the call.
		if b.clk.Since(b.openedAt) < b.openFor {
			//: within the Open window.
			return admission{}, false
		}
		//: cooldown elapsed — the next call is the trial.
		b.state = stateHalfOpen
		b.probing = false
	}
	//: HalfOpen admits one trial; the others are rejected while it runs.
	if b.state == stateHalfOpen {
		//: a trial is already probing the dependency.
		if b.probing {
			//: one trial at a time.
			return admission{}, false
		}
		//: this call is the trial.
		b.probing = true
		//: admitted as the trial of the current generation.
		return admission{generation: b.generation, trial: true}, true
	}
	//: Closed admits the call.
	return admission{generation: b.generation}, true
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
	b.record(adm, outcomeNeutral)
}

// record folds a call outcome into the state machine. An outcome from an
// earlier generation is ignored, and a success closes the breaker only from
// Closed or from its HalfOpen trial. Holds mu.
func (b *circuitBreaker) record(adm admission, res outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: a straggler admitted before the last trip says nothing about now.
	if adm.generation != b.generation {
		//: stale — ignored.
		return
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
		//: only Closed and the HalfOpen trial may close; Open stays Open.
		if b.state == stateClosed || (b.state == stateHalfOpen && adm.trial) {
			//: recovery (or normal operation) resets to Closed.
			b.state = stateClosed
			b.failures = 0
		}
	//: a failure counts in Closed and re-opens from HalfOpen.
	case outcomeFailure:
		b.fail()
	//: a deterministic error leaves the state machine as it was.
	case outcomeNeutral:
	}
}

// fail folds one counted failure into the state machine. Holds mu (via record).
func (b *circuitBreaker) fail() {
	//: dispatch on the state the failure lands in.
	switch b.state {
	//: the probe failed — back to Open with a fresh timer.
	case stateHalfOpen:
		b.trip()
	//: count the failure; trip once the threshold is reached.
	case stateClosed:
		b.failures++
		//: too many consecutive failures opens the circuit.
		if b.failures >= b.threshold {
			//: open the circuit.
			b.trip()
		}
	//: an Open breaker of the same generation has nothing to learn.
	case stateOpen:
	}
}

// trip moves the breaker to Open, stamps the cooldown start and starts a new
// generation, so every call still in flight becomes a straggler. Holds mu.
func (b *circuitBreaker) trip() {
	//: enter Open and start the cooldown clock.
	b.state = stateOpen
	b.openedAt = b.clk.Now()
	b.generation++
	b.probing = false
}
