// Package lifecycle — hosts Phase, the direction a transition moved in.
package lifecycle

import "time"

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

// TransitionValue reports one call the [Lifecycle] made into a component:
// which component, which half of its contract, when it began and ended, and
// what came back.
//
// It reports DECISIONS, not only successes. A Stop abandoned at its budget
// produces one too, with TimedOut set and an Err naming the component — a
// lifecycle that reported only the calls that returned would make a component
// which never stops look exactly like one that stops instantly, and would
// leave "this component overran" provable only by timing the process.
type TransitionValue struct {
	// Name is the component's registered name.
	Name string
	// Phase says whether this was the Start half or the Stop half.
	Phase Phase
	// Begun is the instant the call was made, read from the injected clock.
	Begun time.Time
	// Ended is the instant the Lifecycle stopped waiting for it. On a
	// TimedOut transition that is when the budget expired, NOT when the
	// component eventually returned — the Lifecycle does not know when that
	// was, and reporting a guess would be worse than reporting the fact.
	Ended time.Time
	// TimedOut reports that the component's Stop had not returned when its
	// budget expired. The goroutine was abandoned, not killed; see [Stop].
	TimedOut bool
	// Err is what the component returned, verbatim, so the caller's errors.Is
	// keeps working. A panic is the one exception: it becomes
	// [ComponentPanicked] with the recovered value in a field.
	Err error
}
