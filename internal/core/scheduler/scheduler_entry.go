// Package scheduler — hosts EntryValue, the registration a Scheduler owns.
package scheduler

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
	// same class of claim (ADR 0031).
	AllowOverlap bool
}
