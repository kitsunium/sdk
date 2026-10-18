package app

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// Job is scheduled work: a piece of the daemon's internal loop. It runs on
// the SDK scheduler, whose decisions about time are documented rather than
// emergent: a fire that is due while the previous run of the same job is
// still going is skipped and counted, and a missed deadline is skipped and
// counted, never caught up.
type Job struct {
	nodeBase
	fn func(context.Context) error
	// period is an interval job's, expr a cron job's: what the product
	// declared, read into a schedule at the start, through the jobs
	// subsystem (ADR 0179 step 7) — never at the declaration, which a
	// package's init may run before the subsystem's.
	period   time.Duration
	expr     string
	schedule string
	jobKind  string
}

// Every declares a job that runs every period, first one period after the
// app starts.
//
//go:noinline
func (s *Service) Every(name string, period time.Duration, fn func(context.Context) error) *Job {
	j := s.job(name, fn, "interval", "every "+period.String())
	j.period = period
	return j
}

// Cron declares a job that runs on a five-field POSIX cron expression, in the
// process's local time zone.
//
//go:noinline
func (s *Service) Cron(name, expr string, fn func(context.Context) error) *Job {
	j := s.job(name, fn, "cron", expr)
	j.expr = expr
	return j
}

// job declares a job of the given kind and schedule; Every and Cron call it,
// which the position it records counts on.
func (s *Service) job(name string, fn func(context.Context) error, kind, schedule string) *Job {
	j := &Job{fn: fn, schedule: schedule, jobKind: kind}
	j.kind, j.name = model.KindJob, name
	j.decl = callerFrame(3) // callerFrame ← job ← Every/Cron ← the declaration

	j.body = bodyOf(fn)
	s.add(j, true)
	if fn == nil {
		s.problem(j.decl, j.id, "job.nil", "name", name)
	}
	return j
}

// scheduleOf reads the job's schedule through the jobs subsystem d: its
// period's, or its cron expression's.
func (j *Job) scheduleOf(d *plug.JobDriverConfig) (plug.JobScheduleFunc, error) {
	if j.jobKind == "cron" {
		return d.Cron(j.expr)
	}
	return d.Every(j.period)
}

// check judges the job's schedule at the start, by the SDK's rules: a period
// that is not positive, a cron expression of no five fields. Without the jobs
// subsystem it judges nothing: the start refuses the job for want of it
// (subsystem.go).
func (j *Job) check() []phrase {
	d := plug.Jobs.Load()
	if d == nil {
		return nil
	}
	if _, err := j.scheduleOf(d); err != nil {
		if j.jobKind == "cron" {
			return []phrase{say("job.cron", "name", j.name, "expr", j.expr)}
		}
		return []phrase{say("job.period", "name", j.name)}
	}
	return nil
}

// run is the scheduler's view of the job: one root span per run.
func (j *Job) run(a *App) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, sp := a.begin(ctx, &spanStart{node: j.id, op: model.OpRun, name: j.name})
		err := j.fn(ctx)
		sp.end(err)
		return err
	}
}

// wake is why a scheduled run of the job starts: a period went by, or a
// cron deadline came.
func (j *Job) wake() string {
	if j.jobKind == "cron" {
		return model.WakeDeadline
	}
	return model.WakeInterval
}

// describe fills the graph node out with what the Job declares, and returns
// its edges.
func (j *Job) describe(_ *App, out *model.Node) []model.Edge {
	out.Job = &model.JobInfo{Schedule: j.schedule, Kind: j.jobKind}
	return nil
}
