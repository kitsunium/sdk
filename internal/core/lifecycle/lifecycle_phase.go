// Package lifecycle — hosts Phase, the direction a transition moved in.
package lifecycle

// Phase names which half of a component's contract a [TransitionValue]
// reports. It is a closed set of two: a component goes up or it goes down.
type Phase uint8

// PhaseStart reports a call to a component's [Start].
const PhaseStart Phase = 1

// PhaseStop reports a call to a component's [Stop].
const PhaseStop Phase = 2

// String renders the phase for a log line. An out-of-range value renders as
// "unknown" rather than a number, because a Phase reaching a log with a value
// this package never mints is a defect worth reading as one.
func (p Phase) String() string {
	//: a closed set of two, plus the honest answer for anything else.
	switch p {
	//: the component was brought up.
	case PhaseStart:
		//: the going-up half.
		return "start"
	//: the component was taken down.
	case PhaseStop:
		//: the going-down half.
		return "stop"
	//: a value this package never mints.
	default:
		//: name the absence rather than print a number nobody can look up.
		return "unknown"
	}
}
