package resilience

import "time"

// BreakerPhase is where a circuit breaker stands.
type BreakerPhase uint8

const (
	// BreakerClosed is a breaker that admits every call.
	BreakerClosed BreakerPhase = 1
	// BreakerOpen is a breaker that rejects every call fast.
	BreakerOpen BreakerPhase = 2
	// BreakerHalfOpen is a breaker that admits one trial call at a time.
	BreakerHalfOpen BreakerPhase = 3
)

// String renders the phase.
func (p BreakerPhase) String() string {
	//: one stable spelling per phase; anything else was never minted.
	switch p {
	//: admits every call.
	case BreakerClosed:
		//: stable spelling.
		return "closed"
	//: rejects every call.
	case BreakerOpen:
		//: stable spelling.
		return "open"
	//: admits one trial.
	case BreakerHalfOpen:
		//: stable spelling.
		return "half-open"
	//: the zero value and every value this package never mints.
	default:
		//: a defect worth reading as one.
		return "unknown"
	}
}

// BreakerStateValue is a circuit breaker's reported state.
type BreakerStateValue struct {
	Phase     BreakerPhase
	Isolated  bool
	Calls     int
	Failures  int
	SlowCalls int
	Trips     uint64
	OpenUntil time.Time
}
