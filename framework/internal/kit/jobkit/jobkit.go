package jobkit

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/scheduler"
)

// Enable plugs the jobs' scheduler and their schedules into kit.
func Enable() {
	plug.Jobs.Store(&plug.JobDriverConfig{Every: every, Cron: cron, Open: open})
}

// every is the SDK's schedule of a job that runs every period.
func every(period time.Duration) (plug.JobScheduleFunc, error) {
	s, err := scheduler.Every(period)
	if err != nil {
		return nil, err
	}
	return plug.JobScheduleFunc(s), nil
}

// cron is the SDK's schedule of a five-field cron expression, in the
// process's local time zone.
func cron(expr string) (plug.JobScheduleFunc, error) {
	s, err := scheduler.Parse(expr)
	if err != nil {
		return nil, err
	}
	return plug.JobScheduleFunc(s), nil
}

// open opens the SDK's scheduler over cfg: the app's clock, and kit's ear on
// every decision it takes.
//
// IFACE-PLUGIN: kit reaches the scheduler through plug.JobScheduler; this is
// its implementation.
func open(cfg *plug.JobSchedulerConfig) plug.JobScheduler {
	sc := scheduler.Config{Clock: clockOf(cfg.Clock)}
	if on := cfg.OnResult; on != nil {
		sc.OnResult = func(r scheduler.Result) { on(resultOf(&r)) }
	}
	return runner{s: scheduler.New(sc)}
}

// runner is the SDK's scheduler as kit's core uses it.
type runner struct {
	s scheduler.Scheduler
}

// Add registers entry with the SDK's scheduler.
func (r runner) Add(entry plug.JobEntryValue) error {
	return r.s.Add(scheduler.Entry{Name: entry.Name, Schedule: scheduler.Schedule(entry.Schedule), Job: entry.Run})
}

// Run fires the jobs until ctx ends.
func (r runner) Run(ctx context.Context) error { return r.s.Run(ctx) }

// resultOf is the SDK's report of a decision, as kit's core reads it.
func resultOf(r *scheduler.Result) plug.JobResultValue {
	return plug.JobResultValue{
		Name: r.Name, Scheduled: r.Scheduled, Started: r.Started, Finished: r.Finished,
		Missed: r.Missed, Skipped: r.Skipped, Err: r.Err,
	}
}
