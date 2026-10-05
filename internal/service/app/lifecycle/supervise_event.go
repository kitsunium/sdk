package lifecycle

// The four phases of a supervision. The zero value is none of them, so an
// event that was never filled in reads as "unknown".
const (
	// SupervisionRunStarted: a run of the function began.
	SupervisionRunStarted SupervisionPhase = iota + 1
	// SupervisionRunEnded: a run returned, or panicked.
	SupervisionRunEnded
	// SupervisionRestarting: a run ended early and the next one is
	// scheduled after Delay.
	SupervisionRestarting
	// SupervisionStopped: the supervision is over, and the goroutine
	// that ran it is returning.
	SupervisionStopped
)

// String renders the phase for a log line; a value this package never mints
// renders as "unknown".
func (p SupervisionPhase) String() string {
	//: a closed set of four, plus the honest answer for anything else.
	switch p {
	//: a run began.
	case SupervisionRunStarted:
		//: started.
		return "run started"
	//: a run ended.
	case SupervisionRunEnded:
		//: ended.
		return "run ended"
	//: a restart was scheduled.
	case SupervisionRestarting:
		//: restarting.
		return "restarting"
	//: the supervision ended.
	case SupervisionStopped:
		//: stopped.
		return "stopped"
	//: a value this package never mints.
	default:
		//: name the absence rather than print a number.
		return "unknown"
	}
}
