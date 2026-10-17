package plug

import "context"

// The jobs subsystem's scheduler (ADR 0179 §3, step 7), which
// framework/internal/kit/jobkit implements over the SDK's scheduler.

// JobScheduler fires the app's jobs, each on its schedule, on the app's
// clock: a fire due while the previous run of the same job is going is
// skipped, a missed one is counted, never caught up.
type JobScheduler interface {
	// Add registers a job; it is refused while the scheduler runs, for a
	// name it holds already, and for an entry with no name, run or
	// schedule.
	Add(entry JobEntryValue) error
	// Run fires the jobs until ctx ends, then waits for the runs going.
	Run(ctx context.Context) error
}
