// Package scheduler — declares the sentinel *errs.Error outcomes of the
// domain: the registration refusals, and the parser's refusals of a schedule.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package scheduler

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent configuration fault: the same Add will be refused forever, and the
// fix is a code change at the call site, never a retry. A refused schedule is
// one too: retrying the same expression will be refused identically, and the
// fix is an edit.
const exitConfig int = 78

var (
	// InvalidEntry is returned by Add for an entry that could never run.
	InvalidEntry = errs.Define(CodeInvalidEntry, "INVALID_ENTRY",
		"The scheduler entry is not runnable and was refused",
		"core/app/scheduler: entry has an empty name, a nil Job or a nil Schedule; the field names which",
		errs.WithExitCode(exitConfig))

	// DuplicateJob is returned by Add when the entry's name is already taken.
	DuplicateJob = errs.Define(CodeDuplicateJob, "DUPLICATE_JOB",
		"A job is already registered under that name",
		"core/app/scheduler: entry names are unique per Scheduler; the field carries the name",
		errs.WithExitCode(exitConfig))

	// SchedulerRunning is returned by Add, and by a second Run, while the
	// Scheduler is running. The entry set is fixed for the duration of a Run
	// so the loop needs no wake-up channel and no lock on its hot path; adding
	// between runs is allowed.
	SchedulerRunning = errs.Define(CodeSchedulerRunning, "SCHEDULER_RUNNING",
		"The scheduler is already running",
		"core/app/scheduler: the entry set is frozen for the duration of a Run; add before Run, or after it returns",
		errs.WithExitCode(exitConfig))

	// JobPanicked is the result Err for a Job that panicked. The scheduler
	// recovers it rather than letting it reach the runtime, because one job's
	// bug must not take the process — and every other job — down with it. The
	// recovered value travels as a field; it is never the wrap origin, so a
	// panicking job cannot hijack this code.
	JobPanicked = errs.Define(CodeJobPanicked, "JOB_PANICKED",
		"The scheduled job panicked and was recovered",
		"core/app/scheduler: job panicked; the scheduler recovered and kept running — the fields carry the job name and the panic value")

	// The parser's refusals of a schedule the caller wrote down, raised by
	// internal/service/app/scheduler. They are declared here, with the
	// registration refusals, so that the domain's codes and sentinels are in one
	// place (ADR 0160).

	// InvalidExpression is returned by Parse for a malformed or out-of-range
	// cron field.
	InvalidExpression = errs.Define(CodeInvalidExpression, "INVALID_EXPRESSION",
		"The cron expression is not valid",
		"service/app/scheduler: cron field malformed or out of range; the fields name the field, the item and the accepted range",
		errs.WithExitCode(exitConfig))

	// UnsupportedSyntax is returned by Parse for a construct this subset
	// refuses by name instead of interpreting. Refusing loudly is the whole
	// point: a parser that silently ignores a Quartz "L" would run a job on
	// the wrong day and report success.
	UnsupportedSyntax = errs.Define(CodeUnsupportedSyntax, "UNSUPPORTED_SYNTAX",
		"That cron syntax is outside the accepted subset",
		"service/app/scheduler: five-field POSIX cron plus @yearly/@monthly/@weekly/@daily/@hourly only; the fields name what was rejected",
		errs.WithExitCode(exitConfig))

	// UnreachableSchedule is returned by Parse for a valid expression that
	// matches no instant within the search horizon.
	UnreachableSchedule = errs.Define(CodeUnreachableSchedule, "UNREACHABLE_SCHEDULE",
		"The cron expression matches no date on the calendar",
		"service/app/scheduler: expression parsed but no instant matches within the horizon — e.g. day 31 of a 30-day month",
		errs.WithExitCode(exitConfig))

	// InvalidLocation is returned by ParseInLocation for a nil location.
	InvalidLocation = errs.Define(CodeInvalidLocation, "INVALID_LOCATION",
		"A time zone is required and none was given",
		"service/app/scheduler: ParseInLocation needs a non-nil *time.Location; use Parse for UTC",
		errs.WithExitCode(exitConfig))

	// InvalidInterval is returned by Every for a non-positive duration.
	InvalidInterval = errs.Define(CodeInvalidInterval, "INVALID_INTERVAL",
		"The interval must be greater than zero",
		"service/app/scheduler: Every needs a positive period; a non-positive one is due infinitely often",
		errs.WithExitCode(exitConfig))
)
