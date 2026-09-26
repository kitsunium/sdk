// Package lifecycle — what a supervisor tells its observer.
package lifecycle

import "time"

// SupervisionPhase names what a [SupervisionEventValue] reports.
type SupervisionPhase uint8

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

// SupervisionEventValue is one thing a supervisor did: a run started or
// ended, a restart scheduled, the supervision over. Which fields mean
// something depends on Phase, and the others are zero.
type SupervisionEventValue struct {
	// Err is what an ended run returned: nil when it returned nil before its
	// context ended, RunPanicked for a panic, and nil too when it ended
	// because the supervision was stopping and it returned its context's
	// error — the way out, not a failure.
	Err error
	// At is when the event happened, on the supervisor's clock.
	At time.Time
	// Name is the supervisor's.
	Name string
	// Duration is how long an ended run lasted.
	Duration time.Duration
	// Delay is how long a restart waits before the next run.
	Delay time.Duration
	// Run numbers the run the event is about, from 1; the Stopped event
	// carries the last one.
	Run int
	// Failures is how many runs in a row have ended early, counting this
	// one — the attempt the backoff is computed for.
	Failures int
	// Phase says what happened.
	Phase SupervisionPhase
	// Stopping reports that an ended run ended because the supervision was
	// stopping. Such a run is never restarted.
	Stopping bool
}
