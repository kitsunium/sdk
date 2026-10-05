package health

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

// serving is Status.Serving's body: decl_gen.go writes Status.Serving, from the
// design, as one call of it.
func (s Status) serving() bool {
	//: exactly the two serving states, never "anything but the floor".
	return s == StatusDegraded || s == StatusHealthy
}

// worst is Worst's body: decl_gen.go writes Worst, from the
// design, as one call of it.
func worst(a, b Status) Status {
	//: Status is ordered worst-first, so the more severe verdict is the
	//: smaller value. Degraded can therefore never mask Unhealthy, and a value
	//: no verdict spells is folded in as the most severe one.
	return min(ranked(a), ranked(b))
}

// ranked returns s, or StatusUnhealthy for a value outside the three.
func ranked(s Status) Status {
	//: a verdict nobody can serve on sorts with the one nobody serves on.
	if s > StatusHealthy {
		//: not one of the three.
		return StatusUnhealthy
	}
	//: one of the three, as it is.
	return s
}
