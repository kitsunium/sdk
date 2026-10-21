package scheduler

// OverlapPolicy is what a fire does while the previous run of its entry has
// not returned: [OverlapSkip] (the zero), [OverlapAllow] or
// [OverlapReplace].
type OverlapPolicy uint8

// The three answers to a fire that comes due while the previous run of the
// same entry has not returned — the concurrencyPolicy of a Kubernetes
// CronJob, under the scheduler's names. The zero is the safe one.
const (
	// OverlapSkip does not run the fire and reports it as
	// [ResultValue].Skipped. It is the zero OverlapPolicy: the SDK cannot know
	// whether two copies of a Job are safe side by side.
	OverlapSkip OverlapPolicy = iota
	// OverlapAllow runs the fire beside the previous run: the caller's in-code
	// assertion that concurrent copies are safe. [EntryValue].AllowOverlap
	// says the same.
	OverlapAllow
	// OverlapReplace cancels the previous run's context and runs the fire once
	// that run has returned — never beside it. The cancelled run is reported
	// with [ResultValue].Replaced. A fire that comes due while its own
	// predecessor is still returning replaces the waiting one in turn, so at
	// most one run waits: the newest.
	OverlapReplace
)

// String renders the policy for a log line: "skip", "allow", "replace";
// a value this package does not define renders "unknown".
func (p OverlapPolicy) String() string {
	//: a closed set of three, plus the honest answer for anything else.
	switch p {
	//: the zero.
	case OverlapSkip:
		//: skip.
		return "skip"
	//: side by side.
	case OverlapAllow:
		//: allow.
		return "allow"
	//: the newer one wins.
	case OverlapReplace:
		//: replace.
		return "replace"
	//: a value this package never mints.
	default:
		//: name the absence rather than print a number.
		return "unknown"
	}
}
