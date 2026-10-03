// Package health — hosts Probe, the three questions an orchestrator asks.
package health

// Probe names which of the three questions is being asked. It is a closed set
// of three, and the three are not interchangeable — see the package comment.
//
// The zero value is deliberately not one of them. A Probe is chosen by the
// caller at every call site, and there is no defensible default: answering
// "liveness" to a caller who forgot to say what they wanted is how a
// dependency outage becomes a restart loop.
type Probe uint8

const (
	// ProbeStartup asks "is this process still coming up?". While it answers
	// no, the orchestrator must neither route to the replica nor restart it.
	//
	// The run starts at iota+1, not at iota: the zero is left unclaimed on
	// purpose, so a Probe nobody set is a value this package never mints
	// rather than the startup question answered by default.
	ProbeStartup Probe = iota + 1
	// ProbeReadiness asks "can this replica take traffic right now?". A no
	// removes it from routing and never restarts it. Dependency checks belong
	// here, and only here.
	ProbeReadiness
	// ProbeLiveness asks "is this process irrecoverable?". A no restarts the
	// replica, so only process-local evidence may contribute to it.
	ProbeLiveness
)

// String renders the probe for a log line. An out-of-range value renders as
// "unknown" rather than a number, because a Probe reaching a log with a value
// this package never mints is a defect worth reading as one.
func (p Probe) String() string {
	//: a closed set of three, plus the honest answer for anything else.
	switch p {
	//: the process is coming up.
	case ProbeStartup:
		//: the startup question.
		return "startup"
	//: the process may or may not be able to serve.
	case ProbeReadiness:
		//: the routing question.
		return "readiness"
	//: the process may or may not be salvageable.
	case ProbeLiveness:
		//: the restart question.
		return "liveness"
	//: a value this package never mints, including the zero.
	default:
		//: name the absence rather than pick a probe on the caller's behalf.
		return "unknown"
	}
}
