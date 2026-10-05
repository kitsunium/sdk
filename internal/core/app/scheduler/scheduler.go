package scheduler

import (
	"context"
	"time"
)

// Job is the unit of work a [Scheduler] fires. It MUST honour ctx: the
// scheduler passes the context Run was called with, so cancelling that context
// is how a shutdown reaches a running job. A Job that ignores ctx delays Run's
// return by its own duration — the scheduler waits for it rather than
// abandoning it, because a job still running after the caller believes the
// scheduler stopped is worse than a slow shutdown.
//
// A Job's error is reported through [ResultValue] and is otherwise ignored: a
// failing job never stops the scheduler and never affects another entry.
type Job func(ctx context.Context) error

// Schedule reports the first instant STRICTLY after `after` at which a job is
// due, and whether such an instant exists at all. A false second return means
// the schedule will never fire again; the scheduler disarms that entry and
// keeps running the others.
//
// Implementations MUST be pure — the same `after` always yields the same
// answer — and MUST be safe for concurrent use. The scheduler calls Next
// repeatedly, including several times in one pass when fires were missed, and
// a Schedule returning a value that is not strictly after its argument would
// make that loop spin forever.
type Schedule func(after time.Time) (next time.Time, ok bool)
