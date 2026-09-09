// Package scheduler — declares the sentinel *errs.Error port outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package scheduler

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent configuration fault: the same Add will be refused forever, and the
// fix is a code change at the call site, never a retry.
const exitConfig int = 78

var (
	// InvalidEntry is returned by Add for an entry that could never run.
	InvalidEntry = errs.Define(CodeInvalidEntry, "INVALID_ENTRY",
		"The scheduler entry is not runnable and was refused",
		"core/scheduler: entry has an empty name, a nil Job or a nil Schedule; the field names which",
		errs.WithExitCode(exitConfig))

	// DuplicateJob is returned by Add when the entry's name is already taken.
	DuplicateJob = errs.Define(CodeDuplicateJob, "DUPLICATE_JOB",
		"A job is already registered under that name",
		"core/scheduler: entry names are unique per Scheduler; the field carries the name",
		errs.WithExitCode(exitConfig))

	// SchedulerRunning is returned by Add, and by a second Run, while the
	// Scheduler is running. The entry set is fixed for the duration of a Run
	// so the loop needs no wake-up channel and no lock on its hot path; adding
	// between runs is allowed.
	SchedulerRunning = errs.Define(CodeSchedulerRunning, "SCHEDULER_RUNNING",
		"The scheduler is already running",
		"core/scheduler: the entry set is frozen for the duration of a Run; add before Run, or after it returns",
		errs.WithExitCode(exitConfig))

	// JobPanicked is the result Err for a Job that panicked. The scheduler
	// recovers it rather than letting it reach the runtime, because one job's
	// bug must not take the process — and every other job — down with it. The
	// recovered value travels as a field; it is never the wrap origin, so a
	// panicking job cannot hijack this code.
	JobPanicked = errs.Define(CodeJobPanicked, "JOB_PANICKED",
		"The scheduled job panicked and was recovered",
		"core/scheduler: job panicked; the scheduler recovered and kept running — the fields carry the job name and the panic value")
)
