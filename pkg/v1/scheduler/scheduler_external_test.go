package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/scheduler"
)

// TestFacadeSchedulesThroughTheRealEngine is the end-to-end shape a consumer
// actually writes: parse an expression, register it, run, stop. It exercises
// the facade's delegation rather than re-testing the engine, which
// internal/service/scheduler covers exhaustively.
//
// Lifecycle: one goroutine running Run, joined through the done channel after
// the context is cancelled.
func TestFacadeSchedulesThroughTheRealEngine(t *testing.T) {
	t.Parallel()
	fired := make(chan scheduler.Result, 4)
	sched := scheduler.New(scheduler.Config{
		OnResult: func(result scheduler.Result) { fired <- result },
	})
	//: a one-millisecond interval on the real clock: the point is that the
	//: wiring runs, not that the cadence is exact — that is asserted
	//: deterministically on a ManualClock in internal/service/scheduler.
	every, err := scheduler.Every(time.Millisecond)
	if err != nil {
		t.Fatalf("Every failed: %v", err)
	}
	addErr := sched.Add(scheduler.Entry{
		Name: "tick", Schedule: every,
		Job: func(context.Context) error { return nil },
	})
	if addErr != nil {
		t.Fatalf("Add failed: %v", addErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx) }()
	result := <-fired
	if result.Name != "tick" {
		t.Errorf("result name = %q, want %q", result.Name, "tick")
	}
	cancel()
	if runErr := <-done; runErr != nil {
		t.Errorf("Run returned %v, want nil", runErr)
	}
}

// TestParseIsRefusedAtConstruction pins the distinction the domain is built
// around, from the consumer's side: an unusable expression fails where it is
// written, never at the hour it would have run.
func TestParseIsRefusedAtConstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		expr   string
		reason string
	}{
		{"seconds dialect", "0 0 12 * * *", "UNSUPPORTED_SYNTAX"},
		{"quartz operator", "0 0 L * *", "UNSUPPORTED_SYNTAX"},
		{"never on the calendar", "0 0 30 2 *", "UNREACHABLE_SCHEDULE"},
		{"out of range", "99 * * * *", "INVALID_EXPRESSION"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schedule, err := scheduler.Parse(tc.expr)
			if err == nil {
				t.Fatalf("Parse(%q) was accepted", tc.expr)
			}
			if schedule != nil {
				t.Errorf("Parse(%q) returned a Schedule alongside its error", tc.expr)
			}
			if !errs.HasReason(err, tc.reason) {
				t.Errorf("Parse(%q) = %v, want reason %s", tc.expr, err, tc.reason)
			}
		})
	}
}

// TestAnEmptySchedulerRunsAndStops pins the legitimate-empty case at the
// public edge: it is a scheduler with nothing registered yet, not a
// misconfiguration, and it must not be refused.
//
// Lifecycle: one goroutine running Run, joined through the done channel after
// the context is cancelled.
func TestAnEmptySchedulerRunsAndStops(t *testing.T) {
	t.Parallel()
	sched := scheduler.New(scheduler.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run on an empty scheduler returned %v, want nil", err)
	}
}

// TestSentinelsAreMatchableThroughPkgErrs pins that the re-exported sentinels
// are the same values the engine emits — an alias that had drifted to a copy
// would leave every consumer's errs.HasCode silently false.
func TestSentinelsAreMatchableThroughPkgErrs(t *testing.T) {
	t.Parallel()
	sched := scheduler.New(scheduler.Config{})
	err := sched.Add(scheduler.Entry{})
	if !errs.HasCode(err, mustCode(t, scheduler.InvalidEntry)) {
		t.Errorf("Add(zero entry) = %v, want the re-exported InvalidEntry code", err)
	}
	if !errs.HasReason(err, "INVALID_ENTRY") {
		t.Errorf("Add(zero entry) = %v, want reason INVALID_ENTRY", err)
	}
}

// mustCode reads a sentinel's code through the public accessor, which is the
// only route a consumer has.
func mustCode(t *testing.T, sentinel error) errs.Code {
	t.Helper()
	code, ok := errs.CodeOf(sentinel)
	if !ok {
		t.Fatalf("sentinel %v carries no typed code", sentinel)
	}
	return code
}
