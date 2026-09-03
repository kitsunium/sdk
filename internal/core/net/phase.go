// Package net — the server lifecycle phase.
package net

// Phase is where a server sits in its lifecycle. It is reported by State so an
// operator — or a readiness probe — can tell "not yet listening" apart from
// "listening" and from "draining", which a single boolean cannot express.
type Phase uint8

// The lifecycle phases, in the order a server passes through them.
const (
	// PhaseNew is a constructed server that has not bound anything yet.
	PhaseNew Phase = iota
	// PhaseStarting is binding listeners; some may already be up.
	PhaseStarting
	// PhaseServing is bound and accepting.
	PhaseServing
	// PhaseDraining has stopped accepting and is waiting for in-flight work.
	PhaseDraining
	// PhaseStopped has released every listener.
	PhaseStopped
)

// String renders the phase for logs and status output.
func (p Phase) String() string {
	//: a small dense switch beats a package-level slice that can fall out of
	//: step with the constant block above it.
	switch p {
	//: constructed but not yet bound.
	case PhaseNew:
		//: the canonical lowercase token used in logs and status output.
		return "new"
	//: binding in progress.
	case PhaseStarting:
		//: the canonical lowercase token used in logs and status output.
		return "starting"
	//: bound and accepting.
	case PhaseServing:
		//: the canonical lowercase token used in logs and status output.
		return "serving"
	//: no longer accepting, waiting for in-flight work.
	case PhaseDraining:
		//: the canonical lowercase token used in logs and status output.
		return "draining"
	//: fully released.
	case PhaseStopped:
		//: the canonical lowercase token used in logs and status output.
		return "stopped"
	//: an out-of-range value is reported rather than hidden.
	default:
		//: report the out-of-range value rather than hiding it.
		return "unknown"
	}
}
