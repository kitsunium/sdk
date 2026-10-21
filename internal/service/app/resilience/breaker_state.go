package resilience

import coreres "github.com/kitsunium/sdk/internal/core/app/resilience"

// breakerState is the circuit-breaker state machine value: the phase the core
// publishes, so State reports what the machine holds without a translation.
type breakerState = coreres.BreakerPhase

const (
	// stateClosed passes calls through and counts failures.
	stateClosed = coreres.BreakerClosed
	// stateOpen rejects calls fast until the cooldown elapses, or until Reset
	// when isolated.
	stateOpen = coreres.BreakerOpen
	// stateHalfOpen admits a single trial call to probe recovery.
	stateHalfOpen = coreres.BreakerHalfOpen
)
