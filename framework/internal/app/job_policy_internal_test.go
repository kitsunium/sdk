package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// A job's overlap policy and starting deadline reach the scheduler its
// jobs subsystem opens, as numbers and a duration (plug.JobEntryValue), and
// what the scheduler decides of them — a fire too late, a run replaced — is
// counted on the job's loop. The scheduler here records what it is given and
// fires nothing: the test runs the entries itself.

// recordingJobs is a jobs subsystem whose scheduler records its entries and
// the ear it is given, and fires nothing.
type recordingJobs struct {
	mu       sync.Mutex
	entries  map[string]plug.JobEntryValue
	onResult func(plug.JobResultValue)
}

// Add records entry.
func (r *recordingJobs) Add(entry plug.JobEntryValue) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[entry.Name] = entry
	return nil
}

// Run waits for ctx to end.
func (*recordingJobs) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// entry is the entry recorded for the job name.
func (r *recordingJobs) entry(t *testing.T, name string) plug.JobEntryValue {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok {
		t.Fatalf("no entry %s among %v", name, r.entries)
	}
	return e
}

// withRecordingJobs plugs a recording scheduler in, over the schedules of
// the jobs subsystem the test binary imports, until the test ends.
func withRecordingJobs(t *testing.T) *recordingJobs {
	t.Helper()
	saved := plug.Jobs.Load()
	rec := &recordingJobs{entries: map[string]plug.JobEntryValue{}}
	plug.Jobs.Store(&plug.JobDriverConfig{Every: saved.Every, Cron: saved.Cron, Open: func(cfg *plug.JobSchedulerConfig) plug.JobScheduler {
		rec.onResult = cfg.OnResult
		return rec
	}})
	t.Cleanup(func() { plug.Jobs.Store(saved) })
	return rec
}

// jobsService declares a job that replaces its runs and bounds how late
// they start, one that runs beside itself, and one on the defaults; each
// job's run waits for its context to end.
func jobsService(started chan<- string) *Service {
	svc := NewService("audit", "Jobs, each overlap.")
	run := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			started <- name
			<-ctx.Done()
			return ctx.Err()
		}
	}
	svc.Every("tally", time.Hour, run("tally"), Overlap(OverlapReplace), StartingDeadline(30*time.Second))
	svc.Every("sync", time.Hour, run("sync"), Overlap(OverlapAllow))
	svc.Cron("digest", "0 9 * * *", run("digest"))
	return svc
}

// startJobs runs svc in memory, its configuration files files.
func startJobs(t *testing.T, svc *Service, files fstest.MapFS) *App {
	t.Helper()
	a := NewApp("audit", svc).With(InMemory(), Clock(manualAt()), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false),
		Logs(io.Discard), ConfigFiles(files))
	must(t, a.Start(t.Context()))
	stopAtCleanup(t, a)
	return a
}

// A job's overlap and starting deadline reach the scheduler: from the code,
// and from the configuration, which overrides them.
func TestAJobsOverlapAndDeadlineReachTheScheduler(t *testing.T) {
	rec := withRecordingJobs(t)
	startJobs(t, jobsService(make(chan string, 8)), fstest.MapFS{})
	for name, want := range map[string]plug.JobEntryValue{
		"audit/job/tally":  {Overlap: 2, StartingDeadline: 30 * time.Second},
		"audit/job/sync":   {Overlap: 1},
		"audit/job/digest": {Overlap: 0},
	} {
		if e := rec.entry(t, name); e.Overlap != want.Overlap || e.StartingDeadline != want.StartingDeadline {
			t.Errorf("%s: overlap %d deadline %v, want %d %v", name, e.Overlap, e.StartingDeadline, want.Overlap, want.StartingDeadline)
		}
	}
	rec = withRecordingJobs(t)
	startJobs(t, jobsService(make(chan string, 8)), fstest.MapFS{"config/dev.json": {Data: []byte(
		`{"audit-job-digest-overlap": "replace", "audit-job-digest-starting-deadline": "2m", "audit-job-tally-overlap": "skip"}`)}})
	if e := rec.entry(t, "audit/job/digest"); e.Overlap != 2 || e.StartingDeadline != 2*time.Minute {
		t.Errorf("the configured digest: overlap %d deadline %v", e.Overlap, e.StartingDeadline)
	}
	if e := rec.entry(t, "audit/job/tally"); e.Overlap != 0 {
		t.Errorf("the configured tally: overlap %d", e.Overlap)
	}
}

// A job that runs beside itself runs twice at once; one that skips refuses
// the second run, counted skipped; one that replaces its runs counts the
// run a newer fire cancelled as a run and no failure, a run replaced before
// it started as skipped, and a fire past its starting deadline as missed.
func TestTheSchedulersDecisionsAreCountedOnTheJobsLoop(t *testing.T) {
	rec := withRecordingJobs(t)
	started := make(chan string, 8)
	a := startJobs(t, jobsService(started), fstest.MapFS{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ended := make(chan error, 8)
	runTwice := func(name string) {
		e := rec.entry(t, name)
		go func() { ended <- e.Run(ctx) }()
		<-started
		go func() { ended <- e.Run(ctx) }()
	}

	runTwice("audit/job/sync")
	<-started // the second run of a job that runs beside itself starts
	runTwice("audit/job/digest")
	if err := <-ended; !errors.Is(err, errOverlapped) {
		t.Errorf("a second run of a job that skips: %v, want errOverlapped", err)
	}
	cancel()
	for range 3 {
		if err := <-ended; !errors.Is(err, context.Canceled) {
			t.Errorf("a run cancelled: %v", err)
		}
	}

	tally := rec.entry(t, "audit/job/tally")
	replaced, replace := context.WithCancel(t.Context())
	go func() { ended <- tally.Run(replaced) }()
	<-started
	replace() // a newer fire replaces the run: the scheduler cancels its context
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Errorf("the replaced run: %v", err)
	}
	l := a.loopNamed("audit/job/tally")
	now := a.clock.Now()
	rec.onResult(plug.JobResultValue{Name: "audit/job/tally", Scheduled: now, Started: now, Finished: now, Replaced: true, Err: context.Canceled})
	rec.onResult(plug.JobResultValue{Name: "audit/job/tally", Scheduled: now, Replaced: true})
	rec.onResult(plug.JobResultValue{Name: "audit/job/tally", Scheduled: now, Missed: 2, Expired: true})
	a.mu.Lock()
	runs, errCount, skipped, missed, active := l.Runs, l.Errors, l.Skipped, l.Missed, l.active
	a.mu.Unlock()
	if runs != 1 || errCount != 0 || skipped != 1 || missed != 3 || active != 0 {
		t.Errorf("the tally's loop: %d runs, %d errors, %d skipped, %d missed, %d active; want 1, 0, 1, 3, 0",
			runs, errCount, skipped, missed, active)
	}
}
