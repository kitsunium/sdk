package app

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's jobs through the jobs subsystem (ADR 0179 §1, step 7): the core reads
// every job's schedule from what the product declared, at the start, and
// opens the scheduler that fires them — the SDK's, which
// framework/app/jobs plugs in — with a plug.JobSchedulerConfig, holding it as
// a plug.JobScheduler; it imports none of the SDK's scheduler. The
// scheduler's own loop, the jobs' loops and the workflows' timers stay the
// core's.

// jobsPackage is the package a program imports for its jobs.
const jobsPackage = "github.com/kitsunium/sdk/framework/app/jobs"

// jobsMissing is the refusal of a job in a program that does not import the
// jobs subsystem.
func jobsMissing() error {
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"app: a job needs the jobs subsystem, which the program does not import: "+jobsPackage,
		errs.String("packages", jobsPackage))
}

// jobsLinked reports whether the program imports framework/app/jobs.
func jobsLinked() bool { return plug.Jobs.Load() != nil }

// openScheduler opens the scheduler the app's jobs run on, each added with
// its loop: the jobs subsystem's, or — for an app that declares no job — one
// that fires nothing and waits for its end, as the SDK's does with no entry,
// so that a program without the subsystem links none of it. A job without
// the subsystem — which the start's check makes unreachable — is refused
// with the import it needs.
func (a *App) openScheduler(jobs []*Job) (plug.JobScheduler, error) {
	if len(jobs) == 0 {
		return idleScheduler{}, nil
	}
	d := plug.Jobs.Load()
	if d == nil {
		return nil, jobsMissing()
	}
	sched := d.Open(&plug.JobSchedulerConfig{Clock: pluggedClock{Timed: a.clock}, OnResult: a.onResult})
	for _, j := range jobs {
		schedule, err := j.scheduleOf(d)
		if err != nil {
			return nil, err
		}
		l := a.loop(j.id, j.id, model.LoopJob, j.schedule)
		l.schedule = schedule
		overlap := j.knobs.overlapIn()
		entry := plug.JobEntryValue{
			Name: j.id, Schedule: schedule, Run: a.scheduled(l, j.wake(), overlap, j.run(a)),
			Overlap: overlap.number(), StartingDeadline: j.knobs.deadlineIn(),
		}
		if err := sched.Add(entry); err != nil {
			return nil, err
		}
	}
	return sched, nil
}

// idleScheduler is the scheduler of an app that declares no job: it holds
// no entry and waits for its end.
type idleScheduler struct{}

// Add refuses every entry: an app that declares no job adds none.
//
//ktn:allow-unused-param: plug.JobScheduler passes an entry this scheduler refuses whatever it is
func (idleScheduler) Add(_ plug.JobEntryValue) error { return jobsMissing() }

// Run waits for ctx to end.
func (idleScheduler) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
