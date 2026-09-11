// Package scheduler — hosts ResultValue, the record of one scheduling decision.
package scheduler

import "time"

// ResultValue is what a [Scheduler] reports for every decision it takes about
// an entry — a run that happened, and a fire that was deliberately not run.
// Reporting the second is the point: a scheduler that only reports runs makes
// a permanently-skipped entry indistinguishable from an entry that is doing
// its job.
//
// It is also the SDK's own test instrument. Because every decision emits one,
// a test can WAIT for the decision it expects instead of sleeping to prove a
// negative — which is what lets the scheduler suite assert cadences, missed
// deadlines and overlap without a single time.Sleep.
//
// This is a published concrete shape (pkg/v1/scheduler.Result is an alias), so
// ADR 0040 applies: it may still gain or change a field while the module is
// v0, loudly and in the commit message, and not after v1.
type ResultValue struct {
	// Name is the entry's registered name.
	Name string
	// Scheduled is the due instant this decision stands for. When fires were
	// missed it is the LATEST due instant at or before the wake-up, never the
	// oldest: the scheduler skips rather than catches up, so the run that
	// happens is the current one and the stale ones are what got dropped.
	Scheduled time.Time
	// Started is when the Job was invoked. Zero when Skipped.
	Started time.Time
	// Finished is when the Job returned. Zero when Skipped.
	Finished time.Time
	// Missed counts the due instants that were dropped because they had
	// already passed when the scheduler woke — a machine that slept, a
	// timer that overran, a job that held the slot. It never causes extra
	// runs; it exists so "skip" is not the same thing as "silently drop".
	//
	// It counts only instants missed WITHIN one Run: the scheduler keeps no
	// state across restarts, so fires missed while the process was down are
	// not visible to it and are not counted here.
	Missed int
	// Skipped reports that the fire was not run because the previous run of
	// this entry had not returned and AllowOverlap is false.
	Skipped bool
	// Err is the Job's own error, verbatim and unwrapped, so errors.Is against
	// the caller's own sentinels keeps working. It is [JobPanicked] when the
	// Job panicked and the scheduler recovered.
	Err error
}
