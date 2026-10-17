package jobkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
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
		t.Errorf("the run kit hears: %+v", r)
	}
	if want := time.Date(2026, 10, 17, 8, 1, 0, 0, time.UTC); !r.Scheduled.Equal(want) {
		t.Errorf("scheduled at %v, want %v", r.Scheduled, want)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the run ended: %v", err)
	}
}
