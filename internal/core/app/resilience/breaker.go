package resilience

import "time"

// BreakerPhase is where a circuit breaker stands: BreakerClosed,
// BreakerOpen or BreakerHalfOpen. The zero value is none of them.
type BreakerPhase uint8

const (
	// BreakerClosed is a breaker that admits every call and counts their
	// outcomes. The run starts at iota+1: the zero is left unclaimed, so a
	// BreakerPhase nobody set reads "unknown", never "closed" — a forgotten
	// assignment must not read as a healthy dependency (ADR 0031).
	BreakerClosed BreakerPhase = iota + 1
	// BreakerOpen is a breaker that rejects every call fast with CircuitOpen:
	// until its cooldown elapses after a trip, or until Reset after Isolate.
	BreakerOpen
	// BreakerHalfOpen is a breaker whose cooldown elapsed: it admits ONE trial
	// call at a time, whose success closes it and whose failure opens it again.
	BreakerHalfOpen
)

// String renders the phase for a log line, a readiness body or a metric
// label: "closed", "open", "half-open". The spellings are stable — a
// dashboard parses them — and a value this package never mints renders
// "unknown".
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

// BreakerStateValue is a circuit breaker's state as BreakerState.State
// reports it: its phase and the counts that phase is decided on.
type BreakerStateValue struct {
	// Phase is where the breaker stands.
	Phase BreakerPhase
	// Isolated reports a breaker opened by Isolate: its Phase is
	// BreakerOpen, and it stays so until Reset, whatever the clock says.
	Isolated bool
	// Calls is how many calls the breaker's sliding window holds — the
	// successes and failures recorded within it while Closed. A breaker
	// without a window keeps no such count, and reports zero.
	Calls int
	// Failures is, with a sliding window, how many of Calls failed; without
	// one, how many failures followed one another since the last success,
	// the count FailureThreshold is compared to.
	Failures int
	// SlowCalls is how many of Calls lasted at least the window's
	// SlowAfter, failed or not. Zero without a window.
	SlowCalls int
	// Trips counts the times the breaker opened on its own since it was
	// built — from Closed on its threshold, or from HalfOpen on a failed
	// trial. Isolate is not a trip.
	Trips uint64
	// OpenUntil is when an Open breaker admits its next HalfOpen trial. It
	// is zero unless Phase is BreakerOpen, and zero for an isolated one,
	// which admits nothing until Reset.
	OpenUntil time.Time
}
