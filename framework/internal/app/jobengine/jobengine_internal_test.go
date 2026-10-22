package jobengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/scheduler"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// manual is a test's manual clock as kit's core hands it (plug.Clock).
type manual struct {
	*clock.ManualClock
}

// NewTimer is the manual clock's timer, as plug's.
func (m manual) NewTimer(d time.Duration) plug.Timer { return m.ManualClock.NewTimer(d) }

// NewTicker is the manual clock's ticker, as plug's.
func (m manual) NewTicker(d time.Duration) plug.Ticker { return m.ManualClock.NewTicker(d) }

// A period is the SDK's schedule, one period after each instant, and one
// that is not positive is the SDK's refusal; a cron expression is read in
// five fields, and one that is not is refused.
func TestTheSchedulesAreTheSDKs(t *testing.T) {
	at := time.Date(2026, 10, 17, 8, 30, 0, 0, time.Local)
	s, err := every(time.Minute)
	if err != nil {
		t.Fatalf("every minute: %v", err)
	}
	if next, ok := s(at); !ok || !next.Equal(at.Add(time.Minute)) {
		t.Errorf("every minute after %v: %v %v", at, next, ok)
	}
	if _, err := every(0); !errors.Is(err, scheduler.InvalidInterval) {
		t.Errorf("every 0: %v, want the SDK's InvalidInterval", err)
	}
	c, err := cron("0 9 * * *")
	if err != nil {
		t.Fatalf("cron: %v", err)
	}
	if next, ok := c(at); !ok || next.Hour() != 9 || next.Minute() != 0 || next.Day() != 17 {
		t.Errorf("0 9 * * * after %v: %v %v", at, next, ok)
	}
	if _, err := cron("every tuesday"); err == nil {
		t.Error("a cron expression of no five fields is read")
	}
}

// The scheduler fires a job on the app's clock — a test's manual clock —
// and kit hears the run; it refuses a name twice, and stops when its
// context ends.
func TestTheSchedulerFiresOnTheAppsClock(t *testing.T) {
	mc := clock.NewManualClock(time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC))
	results := make(chan plug.JobResultValue, 4)
	s := open(&plug.JobSchedulerConfig{Clock: manual{mc}, OnResult: func(r plug.JobResultValue) { results <- r }})
	sched, err := every(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	entry := plug.JobEntryValue{Name: "tally", Schedule: sched, Run: func(context.Context) error { return boom }}
	if err := s.Add(entry); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Add(entry); !errors.Is(err, scheduler.DuplicateJob) {
		t.Errorf("a name added twice: %v, want the SDK's DuplicateJob", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	mc.BlockUntil(1)
	mc.Advance(time.Minute)
	r := <-results
	if r.Name != "tally" || r.Skipped || r.Started.IsZero() || !errors.Is(r.Err, boom) {
		t.Errorf("the run the framework hears: %+v", r)
	}
	if want := time.Date(2026, 10, 17, 8, 1, 0, 0, time.UTC); !r.Scheduled.Equal(want) {
		t.Errorf("scheduled at %v, want %v", r.Scheduled, want)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the run ended: %v", err)
	}
}

// A job's overlap policy and starting deadline are the SDK's: a fire the
// scheduler comes to past the deadline is heard expired, never run; a fire
// due while the run of a job that replaces its runs still goes cancels that
// run, which the framework hears replaced; and a policy the SDK does not
// define is the SDK's refusal.
func TestTheOverlapAndTheDeadlineAreTheSDKs(t *testing.T) {
	mc := clock.NewManualClock(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC))
	results := make(chan plug.JobResultValue, 8)
	s := open(&plug.JobSchedulerConfig{Clock: manual{mc}, OnResult: func(r plug.JobResultValue) { results <- r }})
	hourly, err := every(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	if err := s.Add(plug.JobEntryValue{Name: "sync", Schedule: hourly, Overlap: 2, Run: func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}); err != nil {
		t.Fatalf("add a job that replaces its runs: %v", err)
	}
	var lateRuns atomic.Int32
	if err := s.Add(plug.JobEntryValue{Name: "late", Schedule: hourly, StartingDeadline: time.Minute, Run: func(context.Context) error {
		lateRuns.Add(1)
		return nil
	}}); err != nil {
		t.Fatalf("add a job with a deadline: %v", err)
	}
	odd := plug.JobEntryValue{Name: "odd", Schedule: hourly, Overlap: 9, Run: func(context.Context) error { return nil }}
	if err := s.Add(odd); !errors.Is(err, scheduler.InvalidEntry) {
		t.Errorf("an overlap the SDK does not define: %v, want InvalidEntry", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	mc.BlockUntil(1)
	mc.Advance(time.Hour + 2*time.Minute) // the scheduler comes to the 9:00 fires two minutes late
	<-started
	if r := <-results; r.Name != "late" || !r.Expired || !r.Started.IsZero() || lateRuns.Load() != 0 {
		t.Errorf("the late fire: %+v, %d runs; want expired, never run", r, lateRuns.Load())
	}
	mc.BlockUntil(1)
	mc.Advance(58 * time.Minute) // 10:00, on time: the next fire replaces the run still going
	for r := range results {
		if r.Name != "sync" {
			continue
		}
		if !r.Replaced || !errors.Is(r.Err, context.Canceled) || r.Started.IsZero() {
			t.Errorf("the run a newer fire replaced: %+v", r)
		}
		break
	}
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the run ended: %v", err)
	}
}
