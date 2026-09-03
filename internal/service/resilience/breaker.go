// Package resilience — circuit-breaker policy.
package resilience

import (
	"context"
	"sync"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// defaultThreshold is the consecutive-failure count that opens the breaker when
// none is configured.
const defaultThreshold int = 5

// circuitBreaker trips Open after FailureThreshold consecutive failures, rejects
// for OpenDuration, then half-opens for a trial.
type circuitBreaker struct {
	mu        sync.Mutex
	clk       clock.Clock
	retryable func(error) bool
	threshold int
	openFor   time.Duration
	state     breakerState
	failures  int
	openedAt  time.Time
}

// NewCircuitBreaker returns a Runner guarding op with a Closed→Open→HalfOpen
// breaker. Open calls return CircuitOpen until OpenDuration elapses. An error
// rejected by cfg.Retryable is returned verbatim and left out of the state
// machine entirely, so deterministic failures cannot trip the breaker.
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
	//: a fresh breaker starts Closed.
	return &circuitBreaker{clk: clk, retryable: cfg.Retryable, threshold: threshold, openFor: cfg.OpenDuration}
}

// Run rejects fast when Open, else runs op and records the outcome. A
// deterministic error (one the classifier rejects) is passed through without
// touching the state machine.
func (b *circuitBreaker) Run(ctx context.Context, op coreres.Operation) error {
	//: gate on the current state; a closed gate rejects fast.
	if !b.allow() {
		//: the breaker is Open within its cooldown.
		return wrapAs(coreres.CircuitOpen, nil)
	}
	//: run the guarded operation.
	err := op(ctx)
	//: a deterministic error says nothing about the dependency's health.
	if err != nil && !isRetryable(b.retryable, err) {
		//: neither a failure nor a success — the state machine stays untouched.
		return err
	}
	//: fold the health signal into the state machine.
	b.record(err == nil)
	//: propagate the operation's own outcome.
	return err
}

// allow reports whether a call may proceed, transitioning Open→HalfOpen once the
// cooldown elapses. Holds mu.
func (b *circuitBreaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: an Open breaker only admits once its cooldown has elapsed.
	if b.state == stateOpen {
		//: still cooling down — reject the call.
		if b.clk.Since(b.openedAt) < b.openFor {
			//: within the Open window.
			return false
		}
		//: cooldown elapsed — admit one trial call.
		b.state = stateHalfOpen
	}
	//: Closed and HalfOpen both admit the call.
	return true
}

// record folds a call outcome into the state machine. Holds mu.
func (b *circuitBreaker) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: a success closes the breaker and clears the failure count.
	if success {
		//: recovery (or normal operation) resets to Closed.
		b.state = stateClosed
		b.failures = 0
		//: nothing more to do on success.
		return
	}
	//: a failed trial in HalfOpen re-opens immediately.
	if b.state == stateHalfOpen {
		//: the probe failed — back to Open with a fresh timer.
		b.trip()
		//: re-opened — stop here.
		return
	}
	//: count the failure; trip once the threshold is reached.
	b.failures++
	//: too many consecutive failures opens the circuit.
	if b.failures >= b.threshold {
		//: open the circuit.
		b.trip()
	}
}

// trip moves the breaker to Open and stamps the cooldown start. Holds mu.
func (b *circuitBreaker) trip() {
	//: enter Open and start the cooldown clock.
	b.state = stateOpen
	b.openedAt = b.clk.Now()
}
