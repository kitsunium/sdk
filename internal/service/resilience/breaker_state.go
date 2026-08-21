// Package resilience — circuit-breaker state machine values.
package resilience

// breakerState is the circuit-breaker state machine value.
type breakerState int

const (
	// stateClosed passes calls through and counts failures.
	stateClosed breakerState = iota
	// stateOpen rejects calls fast until the cooldown elapses.
	stateOpen
	// stateHalfOpen admits a single trial call to probe recovery.
	stateHalfOpen
)
