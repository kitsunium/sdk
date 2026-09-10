// Package health — hosts ResultValue and ReportValue, what a probe answers
// with.
package health

import "time"

// ResultValue is one check's answer.
//
// It reports DECISIONS, not only outcomes. A check abandoned at its budget
// produces one, with TimedOut set — a report that only carried the checks that
// returned would make a wedged dependency look exactly like one that was never
// registered.
type ResultValue struct {
	// Name is the check's registered name.
	Name string
	// Status is this check's verdict. A failing check is StatusUnhealthy, or
	// StatusDegraded when it was registered NonCritical.
	Status Status
	// TimedOut reports that the check had not answered when its budget
	// expired. It is a FAILURE, not an unknown — see the package README and
	// ADR 0060 §D4 — and this flag is what distinguishes "the dependency said
	// no" from "the dependency said nothing".
	TimedOut bool
	// Cached reports that this answer was not measured during this probe: it
	// is a previous success replayed within the check's MaxAge window. Age
	// says how old it is.
	Cached bool
	// At is the instant the answer was MEASURED, read from the injected
	// clock. On a cached result that is when the original run finished, not
	// when the probe was served — reporting the probe's own instant would
	// erase the very staleness the cache introduced.
	At time.Time
	// Took is how long the measurement took. On an abandoned check it is the
	// budget, because the engine does not know when the check eventually
	// returned and reporting a guess would be worse than reporting the fact.
	Took time.Duration
	// Err is what the check returned, VERBATIM, so the caller's errors.Is
	// keeps working and an observation hook can log its Private half.
	//
	// It is never rendered into an HTTP body as-is. A handler renders the
	// deepest errs Public it can find and a fixed SDK string otherwise, so a
	// driver message carrying a host, a port or a query never reaches a probe
	// response. See internal/service/health §"what the handler hides".
	Err error
}

// Age returns how stale this answer is as of now, which is zero for anything
// measured during the probe that reported it.
//
// It takes the instant rather than reading a clock so that the value type
// keeps no time source of its own: the engine already has an injected one, and
// a second, hidden, wall-clock read inside a domain value is exactly the thing
// that makes a staleness assertion untestable.
func (r ResultValue) Age(now time.Time) time.Duration {
	//: an unstamped result has no age to report; claiming one would invent a
	//: staleness out of the zero time.
	if r.At.IsZero() {
		//: nothing measured, nothing stale.
		return 0
	}
	age := now.Sub(r.At)
	//: a clock that moved backwards (NTP, a manual clock in a test) must not
	//: produce a negative age that reads as "measured in the future".
	if age < 0 {
		//: floor at zero rather than propagate the anomaly.
		return 0
	}
	//: how old the answer is.
	return age
}

// ReportValue is one probe's whole answer: the aggregate, and every check that
// contributed to it.
//
// Results is empty for a probe that short-circuited — a readiness probe while
// the process is starting or draining, a liveness probe while it is starting.
// That emptiness is the honest report: those probes deliberately ran nothing,
// and listing checks with stale verdicts would suggest otherwise.
type ReportValue struct {
	// Probe says which question was asked.
	Probe Probe
	// Status is the aggregate: the worst of Results, or the short-circuit
	// verdict when Results is empty. A probe with no checks at all is
	// StatusHealthy — the process answering IS the evidence (ADR 0031).
	Status Status
	// At is the instant the probe was answered, read from the injected clock.
	At time.Time
	// Results carries one entry per check that contributed, in registration
	// order — never in completion order, which would make two identical
	// probes render differently and a diff of two responses unreadable.
	Results []ResultValue
}
