package plug

import (
	"context"
	"sync/atomic"
	"time"
)

// The jobs subsystem's contract (ADR 0179 §3, step 7): what kit's core asks
// of the scheduler its jobs run on. framework/kit/jobs sets the hook and
// framework/internal/kit/jobkit implements it over the SDK's scheduler. The
// core reads it and imports none of the SDK's scheduler: a job's schedule is
// read from what the product declared — a period, a cron expression — at
// the start, through the hook, never at the declaration, which a package's
// init may run before the subsystem's; the scheduler is opened once per run
// with a JobSchedulerConfig and held as a JobScheduler. The values are here,
// the scheduler in jobs_interface.go.

// Jobs is the jobs subsystem: the schedules of kit's jobs and the scheduler
// that fires them; nil until framework/kit/jobs is imported. A product that
// declares a job without it is refused at the start (ADR 0179 §6).
var Jobs atomic.Pointer[JobDriverConfig]

// JobDriverConfig is what framework/kit/jobs plugs into kit: how a job's
// schedule is read from its declaration, and how the scheduler that fires
// the app's jobs opens.
type JobDriverConfig struct {
	// Every is the schedule of a job that runs every period, first one
	// period after the scheduler starts; a period that is not positive is
	// refused.
	Every func(period time.Duration) (JobScheduleFunc, error)
	// Cron is the schedule of a five-field POSIX cron expression, in the
	// process's local time zone; an expression it cannot read is refused.
	Cron func(expr string) (JobScheduleFunc, error)
	// Open opens the scheduler the app's jobs run on, as cfg says, with no
	// entry yet.
	Open func(cfg *JobSchedulerConfig) JobScheduler
}

// JobScheduleFunc says when a job runs next after an instant: the instant,
// and false when it never runs again.
type JobScheduleFunc func(after time.Time) (next time.Time, ok bool)

// JobSchedulerConfig is the scheduler the app's jobs run on: the app's clock
// and what kit hears of each fire.
type JobSchedulerConfig struct {
	// Clock is the app's clock, which the scheduler stamps and waits on.
	Clock Clock
	// OnResult hears every decision of the scheduler — each run, with the
	// job's own error, and each fire skipped or missed —, one at a time.
	OnResult func(res JobResultValue)
}

// JobEntryValue is one job as the scheduler fires it: its name, its
// schedule, and the run.
type JobEntryValue struct {
	// Name names the job; the scheduler refuses a name it holds already.
	Name string
	// Schedule says when the job runs next.
	Schedule JobScheduleFunc
	// Run runs the job once; its error is the result's.
	Run func(ctx context.Context) error
}

// JobResultValue is one decision of the scheduler: a run of a job, or a fire
// it skipped.
type JobResultValue struct {
	// Name is the job's.
	Name string
	// Scheduled is the instant the fire was due.
	Scheduled time.Time
	// Started and Finished bound the run; zero for a fire that did not run.
	Started, Finished time.Time
	// Missed counts the fires that came due while the scheduler could not
	// fire them, skipped rather than caught up.
	Missed int
	// Skipped says the fire did not run: a run of the same job was going.
	Skipped bool
	// Err is the run's error.
	Err error
}
