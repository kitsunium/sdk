// Package kit — jobs: scheduled work run by the daemon's loop.
package kit

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/scheduler"
)

// Job is scheduled work: a piece of the daemon's internal loop. It runs on
// the SDK scheduler, whose decisions about time are documented rather than
// emergent: a fire that is due while the previous run of the same job is
// still going is skipped and counted, and a missed deadline is skipped and
// counted, never caught up.
type Job struct {
	nodeBase
	fn       func(context.Context) error
	sched    scheduler.Schedule
	schedule string
	jobKind  string
}

// Every declares a job that runs every period, first one period after the
// app starts.
//
//go:noinline
func (s *Service) Every(name string, period time.Duration, fn func(context.Context) error) *Job {
	j := s.job(name, fn, "interval", "every "+period.String())
	sched, err := scheduler.Every(period)
	if err != nil {
		s.problem(j.decl, j.id, "job.period", "name", name)
	}
	j.sched = sched
	return j
}

// Cron declares a job that runs on a five-field POSIX cron expression, in the
// process's local time zone.
//
//go:noinline
func (s *Service) Cron(name, expr string, fn func(context.Context) error) *Job {
	j := s.job(name, fn, "cron", expr)
	sched, err := scheduler.Parse(expr)
	if err != nil {
		s.problem(j.decl, j.id, "job.cron", "name", name, "expr", expr)
	}
	j.sched = sched
	return j
}

// job declares a job of the given kind and schedule; Every and Cron call it,
// which the position it records counts on.
func (s *Service) job(name string, fn func(context.Context) error, kind, schedule string) *Job {
	j := &Job{fn: fn, schedule: schedule, jobKind: kind}
	j.kind, j.name = model.KindJob, name
	j.decl = callerFrame(3) // callerFrame ← job ← Every/Cron ← the declaration

	if p, _ := funcInfo(fn); p.file != "" {
		j.body = &p
	}
	s.add(j, true)
	if fn == nil {
		s.problem(j.decl, j.id, "job.nil", "name", name)
	}
	return j
}

// run is the scheduler's view of the job: one root span per run.
func (j *Job) run(a *App) scheduler.Job {
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
