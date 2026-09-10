// Package health — hosts Status, the verdict a check and a probe both carry,
// and the aggregation rule that turns the first into the second.
package health

// Status is the verdict of one check or of a whole probe. It is a CLOSED set
// of three, ordered worst-first so that aggregating a set is `min` and the
// zero value is the conservative answer.
//
// That ordering is deliberate and load-bearing (ADR 0031). A Status is
// reachable as a struct field, and if StatusHealthy were the zero value then a
// forgotten assignment — anywhere, in the SDK or in a caller's own reporting —
// would read as "everything is fine". A forgotten assignment now reads as
// "unhealthy", which is wrong in the direction that gets noticed.
type Status uint8

const (
	// StatusUnhealthy is a failing critical check, or a probe with one. The
	// replica must not take traffic (readiness) or is irrecoverable
	// (liveness). It is the ZERO VALUE; see the type comment.
	StatusUnhealthy Status = iota
	// StatusDegraded is a failing NON-critical check, or a probe with one and
	// no critical failure. It is a SERVING state: the replica keeps taking
	// traffic. A degraded replica that stopped serving would make
	// "non-critical" mean nothing.
	StatusDegraded
	// StatusHealthy is every check passing — including the case where there
	// are no checks at all (ADR 0031: an empty registry is legitimate, and
	// the process answering the probe is the evidence).
	StatusHealthy
)

// String renders the status for a log line and for an HTTP body. The three
// spellings are lowercase and stable: an operator greps them and a dashboard
// parses them, so they are part of the contract, not a formatting detail.
func (s Status) String() string {
	//: a closed set of three, plus the honest answer for anything else.
	switch s {
	//: every check passed, or there were none.
	case StatusHealthy:
		//: the serving, unqualified answer.
		return "healthy"
	//: a non-critical check failed; still serving.
	case StatusDegraded:
		//: serving, with a caveat the operator can act on later.
		return "degraded"
	//: a critical check failed.
	case StatusUnhealthy:
		//: not serving.
		return "unhealthy"
	//: a value this package never mints.
	default:
		//: name the absence rather than print a number nobody can look up.
		return "unknown"
	}
}

// Serving reports whether a probe carrying this status should keep receiving
// traffic — which is exactly the question an HTTP status code answers.
//
// Degraded serves. That is the whole meaning of marking a check non-critical:
// if a degraded replica were removed from routing, a "non-critical" cache
// outage would take the fleet down just as thoroughly as a critical one.
func (s Status) Serving() bool {
	//: the two serving states are the two above the floor.
	return s != StatusUnhealthy
}

// Worst returns the more severe of two statuses, which is the SDK's whole
// aggregation rule: a set is as healthy as its least healthy member.
//
// It exists as a named function rather than an inline min so the rule has one
// definition and one test, and so a reader looking for "how does one failing
// check affect the whole probe" finds a function rather than an idiom.
func Worst(a, b Status) Status {
	//: Status is ordered worst-first, so the more severe verdict is the
	//: smaller value. Degraded can therefore never mask Unhealthy.
	return min(a, b)
}
