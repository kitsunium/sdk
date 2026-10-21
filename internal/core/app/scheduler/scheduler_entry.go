package scheduler

import "time"

// EntryValue is one registered pairing of a [Schedule] with a [Job], under a
// name that identifies it in every [ResultValue] and every error field.
//
// The zero value is not runnable and is refused by Add ([InvalidEntry]) rather
// than accepted and silently never fired.
type EntryValue struct {
	// Name identifies the entry in results and errors. It must be non-empty
	// and unique within one Scheduler.
	Name string
	// Schedule decides when the Job is due.
	Schedule Schedule
	// Job is the work to run.
	Job Job
	// AllowOverlap permits a fire while the previous run of THIS entry has not
	// returned. The zero value (false) skips that fire and reports it as
	// [ResultValue.Skipped] — the safe default, because the SDK cannot know
	// whether two concurrent copies of a Job are safe and the symptom of
	// getting it wrong is a duplicated side effect rather than an error.
	// Setting it is the caller's in-code assertion that concurrent copies are
	// safe, the same instrument resilience.HedgeConfig.Idempotent uses for the
	// same class of claim (ADR 0031). It is OverlapAllow written as a bool,
	// kept because it was there first; set with any Overlap but
	// OverlapSkip or OverlapAllow, Add refuses the entry.
	AllowOverlap bool
	// Overlap is what a fire does while the previous run of THIS entry has
	// not returned: OverlapSkip — the zero — skips it, OverlapAllow runs it
	// beside, OverlapReplace cancels the previous run and runs this one
	// once it has returned.
	Overlap OverlapPolicy
	// StartingDeadline bounds how late a fire may start. When the scheduler
	// wakes after a due instant — a machine that slept, a timer that
	// overran — it runs the latest instant missed, however late; with a
	// positive StartingDeadline, an instant more than StartingDeadline in
	// the past is not run but reported with [ResultValue].Expired, as
	// Kubernetes' startingDeadlineSeconds does. Zero, or less, is no
	// deadline, as before the field existed.
	//
	// It is how a job whose run is worth something only near its instant
	// — a report for 02:00, a rotation tied to the hour — says so: a run
	// long after the fact is a surprise, not a catch-up.
	StartingDeadline time.Duration
}
