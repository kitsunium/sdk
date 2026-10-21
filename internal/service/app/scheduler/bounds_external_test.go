// Package scheduler_test — the bounds of #482: a StartingDeadline past which a
// late fire is reported Expired instead of run, and the Replace overlap
// policy, all on the harness's ManualClock.
package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/app/scheduler"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestStartingDeadlineExpiresAFireTooLate pins the deadline: a wake-up 31
// minutes after the latest due instant, past a 30-minute deadline, reports
// that instant Expired with the older ones Missed and runs nothing; the next
// instant, reached on time, runs.
func TestStartingDeadlineExpiresAFireTooLate(t *testing.T) {
	t.Parallel()
	var runs atomic.Int32
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "report", Schedule: everySchedule(t, time.Hour), StartingDeadline: 30 * time.Minute,
		Job: func(context.Context) error { runs.Add(1); return nil },
	})
	h.start(t)
	h.tick(5*time.Hour + 31*time.Minute)
	expired := h.next(t)
	if !expired.Expired || expired.Skipped || expired.Missed != 4 {
		t.Fatalf("late fire = %+v, want Expired with 4 missed", expired)
	}
	if !expired.Scheduled.Equal(engineOrigin.Add(5*time.Hour)) || !expired.Started.IsZero() {
		t.Fatalf("late fire = %+v, want the 5h instant, never started", expired)
	}
	h.tick(29 * time.Minute)
	onTime := h.next(t)
	if onTime.Expired || onTime.Started.IsZero() || runs.Load() != 1 {
		t.Fatalf("on-time fire = %+v (runs %d), want it run once", onTime, runs.Load())
	}
}

// TestStartingDeadlineRunsAFireLateWithinIt pins the other side: a wake-up
// within the deadline runs the latest instant, as without one.
func TestStartingDeadlineRunsAFireLateWithinIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "report", Schedule: everySchedule(t, time.Hour), StartingDeadline: 30 * time.Minute,
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	h.tick(time.Hour + 30*time.Minute)
	result := h.next(t)
	if result.Expired || result.Started.IsZero() {
		t.Fatalf("fire late by exactly the deadline = %+v, want it run", result)
	}
}

// TestOverlapReplaceCancelsTheRunStillGoing pins Replace: the fire that comes
// due while the previous run is still going cancels that run's context, the
// cancelled run is reported Replaced with its own error, and the new run starts
// only once it has returned — two copies never run at once.
func TestOverlapReplaceCancelsTheRunStillGoing(t *testing.T) {
	t.Parallel()
	started := make(chan int32, resultBuffer)
	var running, calls atomic.Int32
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "sync", Schedule: everySchedule(t, time.Hour), Overlap: coresched.OverlapReplace,
		Job: func(ctx context.Context) error {
			if running.Add(1) != 1 {
				t.Error("two runs of a Replace entry overlapped")
			}
			defer running.Add(-1)
			started <- calls.Add(1)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	h.start(t)
	h.tick(time.Hour)
	if n := <-started; n != 1 {
		t.Fatalf("run %d started, want 1", n)
	}
	h.tick(time.Hour)
	replaced := h.next(t)
	if !replaced.Replaced || !errors.Is(replaced.Err, context.Canceled) || replaced.Started.IsZero() {
		t.Fatalf("first run = %+v, want Replaced, cancelled, having run", replaced)
	}
	if !replaced.Scheduled.Equal(engineOrigin.Add(time.Hour)) {
		t.Fatalf("first run scheduled for %s, want +1h", replaced.Scheduled)
	}
	if n := <-started; n != 2 {
		t.Fatalf("run %d started, want 2", n)
	}
	h.stop(t)
	last := h.next(t)
	if last.Replaced || !last.Scheduled.Equal(engineOrigin.Add(2*time.Hour)) {
		t.Fatalf("second run = %+v, want a plain run of the +2h fire, ended by the stop", last)
	}
}

// TestOverlapReplaceReplacesAWaitingRun pins the bound on what waits: a run
// that ignores its cancellation holds the entry, the fire after it waits, and
// a third fire replaces the waiting one — which never starts and is reported
// Replaced — so only the newest runs once the first returns.
func TestOverlapReplaceReplacesAWaitingRun(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	entered := make(chan struct{}, resultBuffer)
	var calls atomic.Int32
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "stubborn", Schedule: everySchedule(t, time.Hour), Overlap: coresched.OverlapReplace,
		Job: func(context.Context) error {
			entered <- struct{}{}
			if calls.Add(1) == 1 {
				<-release
			}
			return nil
		},
	})
	h.start(t)
	h.tick(time.Hour)
	<-entered
	h.tick(time.Hour)
	h.tick(time.Hour)
	//: the loop re-arms only once fire() returned, so the third fire has
	//: replaced the waiting one before the first run is let go.
	h.clk.BlockUntil(1)
	close(release)
	first := h.next(t)
	if !first.Replaced || first.Started.IsZero() || !first.Scheduled.Equal(engineOrigin.Add(time.Hour)) {
		t.Fatalf("first run = %+v, want the +1h run, Replaced", first)
	}
	waiting := h.next(t)
	if !waiting.Replaced || !waiting.Started.IsZero() || !waiting.Scheduled.Equal(engineOrigin.Add(2*time.Hour)) {
		t.Fatalf("waiting run = %+v, want the +2h fire Replaced before it started", waiting)
	}
	newest := h.next(t)
	if newest.Replaced || newest.Started.IsZero() || !newest.Scheduled.Equal(engineOrigin.Add(3*time.Hour)) {
		t.Fatalf("newest run = %+v, want the +3h fire run", newest)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("the job ran %d times, want 2 — the waiting fire never runs", got)
	}
}

// TestAddRefusesAnOverlapItCannotHonour pins the refusals: AllowOverlap beside
// OverlapReplace, and an Overlap beyond the three, are InvalidEntry naming the
// field; AllowOverlap beside OverlapAllow or the zero is accepted.
func TestAddRefusesAnOverlapItCannotHonour(t *testing.T) {
	t.Parallel()
	job := func(context.Context) error { return nil }
	h := newHarness(t)
	for _, c := range []struct {
		entry coresched.EntryValue
		field string
	}{
		{coresched.EntryValue{Name: "both", AllowOverlap: true, Overlap: coresched.OverlapReplace}, "AllowOverlap"},
		{coresched.EntryValue{Name: "unknown", Overlap: 3}, "Overlap"},
	} {
		c.entry.Schedule, c.entry.Job = everySchedule(t, time.Hour), job
		err := h.sched.Add(c.entry)
		if !kerrs.HasCode(err, coresched.CodeInvalidEntry) {
			t.Fatalf("Add(%q) = %v, want INVALID_ENTRY", c.entry.Name, err)
		}
		if got := stringField(err, "field"); got != c.field {
			t.Fatalf("Add(%q) field = %q, want %q", c.entry.Name, got, c.field)
		}
	}
	for _, overlap := range []coresched.OverlapPolicy{coresched.OverlapSkip, coresched.OverlapAllow} {
		entry := coresched.EntryValue{
			Name: "allow-" + overlap.String(), AllowOverlap: true, Overlap: overlap,
			Schedule: everySchedule(t, time.Hour), Job: job,
		}
		if err := h.sched.Add(entry); err != nil {
			t.Fatalf("Add(AllowOverlap with %v) = %v, want accepted", overlap, err)
		}
	}
}

// stringField returns err's field key as a string, or "".
func stringField(err error, key string) string {
	for _, field := range kerrs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}
