package scheduler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcsched "github.com/kitsunium/sdk/internal/service/scheduler"
)

// TestZeroConfigIsAWorkingScheduler pins both fallbacks at once: a nil Clock
// becomes the wall clock and a nil OnResult becomes no observation. Neither is
// an inert configuration, so neither is refused (ADR 0031) — but the second
// does mean a job's error goes nowhere, which is why Config.OnResult says so
// in as many words.
//
// Nothing is advanced here and nothing fires: the assertion is that Run starts
// on the real clock and stops cleanly, which needs no elapsed time at all.
//
// Lifecycle: one goroutine running Run, joined through the done channel after
// the context is cancelled.
func TestZeroConfigIsAWorkingScheduler(t *testing.T) {
	t.Parallel()
	sched := svcsched.New(svcsched.Config{})
	schedule, err := svcsched.Every(time.Hour)
	if err != nil {
		t.Fatalf("Every failed: %v", err)
	}
	addErr := sched.Add(coresched.EntryValue{
		Name: "wall-clock", Schedule: schedule,
		Job: func(context.Context) error { return nil },
	})
	if addErr != nil {
		t.Fatalf("Add failed: %v", addErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx) }()
	cancel()
	if runErr := <-done; runErr != nil {
		t.Errorf("Run returned %v, want nil", runErr)
	}
}

// TestJobsRunWithoutAResultHook pins that the observation hook is optional in
// practice and not just in the doc: the work still happens when nobody is
// watching. The job itself is the witness, which is the only witness there is
// once OnResult is nil.
func TestJobsRunWithoutAResultHook(t *testing.T) {
	t.Parallel()
	ran := make(chan time.Time, 1)
	h := newHarness(t)
	//: rebuild the scheduler without a hook, on the same ManualClock.
	h.sched = svcsched.New(svcsched.Config{Clock: h.clk})
	schedule := everySchedule(t, time.Hour)
	h.add(t, coresched.EntryValue{
		Name: "unobserved", Schedule: schedule,
		Job: func(context.Context) error {
			ran <- h.clk.Now()
			return nil
		},
	})
	h.start(t)
	h.tick(time.Hour)
	if got, want := <-ran, engineOrigin.Add(time.Hour); !got.Equal(want) {
		t.Errorf("job ran at %s, want %s", got, want)
	}
}

// TestIndependentEntriesKeepTheirOwnCadence pins that entries do not interfere:
// a slower one is simply not due when a faster one fires, and an EXHAUSTED one
// stops firing without taking its neighbour with it.
func TestIndependentEntriesKeepTheirOwnCadence(t *testing.T) {
	t.Parallel()
	once := engineOrigin.Add(time.Hour)
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "hourly", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.add(t, coresched.EntryValue{
		Name: "once",
		Schedule: func(after time.Time) (time.Time, bool) {
			//: due at +1h, then exhausted forever.
			if after.Before(once) {
				return once, true
			}
			return time.Time{}, false
		},
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	//: both are due at +1h, so the first tick reports two decisions.
	h.tick(time.Hour)
	first := map[string]bool{h.next(t).Name: true, h.next(t).Name: true}
	if !first["hourly"] || !first["once"] {
		t.Fatalf("first tick fired %v, want both entries", first)
	}
	//: from here only the hourly entry is armed; the exhausted one is asked
	//: about on every pass and answers "not due" rather than misbehaving.
	for hour := 2; hour <= 3; hour++ {
		h.tick(time.Hour)
		result := h.next(t)
		if result.Name != "hourly" {
			t.Fatalf("hour %d fired %q, want only the hourly entry", hour, result.Name)
		}
	}
}

// TestARefusalTruncatesTheEchoedExpression pins the log-bomb guard. Echoing
// the caller's own text is what makes a parse refusal actionable, but an
// unbounded echo turns one bad configuration line into an unreadable log
// entry, so the echo is clipped and the clip is marked.
func TestARefusalTruncatesTheEchoedExpression(t *testing.T) {
	t.Parallel()
	//: a single out-of-range minute item, far longer than the echo budget.
	long := strings.Repeat("9", 200)
	_, err := svcsched.Parse(long + " * * * *")
	if !kerrs.HasCode(err, svcsched.CodeInvalidExpression) {
		t.Fatalf("Parse = %v, want INVALID_EXPRESSION", err)
	}
	var typed *kerrs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("err %v is not an *errs.Error", err)
	}
	found := false
	for _, field := range typed.Fields() {
		if field.Key() != "item" {
			continue
		}
		found = true
		value := field.StringValue()
		if len([]rune(value)) > 65 || !strings.HasSuffix(value, "…") {
			t.Errorf("item field is %d runes and ends %q; want it clipped and marked",
				len([]rune(value)), value)
		}
	}
	if !found {
		t.Error("refusal carried no item field; the caller cannot tell which part was rejected")
	}
}
