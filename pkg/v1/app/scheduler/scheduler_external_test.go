package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/app/lock"
	"github.com/kitsunium/sdk/pkg/v1/app/scheduler"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/cache"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/session"
)

// TestFacadeSchedulesThroughTheRealEngine is the end-to-end shape a consumer
// actually writes: parse an expression, register it, run, stop. It exercises
// the facade's delegation rather than re-testing the engine, which
// internal/service/app/scheduler covers exhaustively.
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
	//: deterministically on a ManualClock in internal/service/app/scheduler.
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

// The four doubles below are pkg/v1/clock's own test doubles, repeated here
// because the configurations they are handed to are this module's and the data
// and security modules', above clock's (ADR 0171): the assertions live with the
// highest of them.

// handTimer is a Timer written from outside the SDK. Returning it from
// NewTimer is only possible because clock.Timer is an alias: a locally
// declared interface with these three methods is a DIFFERENT named type, and
// Go compares method signatures by type identity.
type handTimer struct{ ch chan time.Time }

func (t *handTimer) C() <-chan time.Time      { return t.ch }
func (t *handTimer) Stop() bool               { return true }
func (t *handTimer) Reset(time.Duration) bool { return true }

// handTicker is the Ticker half of the same argument.
type handTicker struct{ ch chan time.Time }

func (t *handTicker) C() <-chan time.Time { return t.ch }
func (t *handTicker) Stop()               {}
func (t *handTicker) Reset(time.Duration) {}

// handClock is a complete clock.Timed — Now, Since, After, NewTimer, NewTicker
// and Sleep — written with no access to internal/. Before pkg/v1/clock existed
// this type could not be spelled at all: NewTicker had to return clock.Ticker,
// and clock.Ticker had no public name.
type handClock struct{ now time.Time }

func (c *handClock) Now() time.Time                  { return c.now }
func (c *handClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }
func (c *handClock) Sleep(time.Duration)             {}

func (c *handClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

func (c *handClock) NewTimer(time.Duration) clock.Timer {
	return &handTimer{ch: make(chan time.Time, 1)}
}

func (c *handClock) NewTicker(time.Duration) clock.Ticker {
	return &handTicker{ch: make(chan time.Time, 1)}
}

// readOnlyClock is the two-method half of the port. clock.Clock is frozen at
// Now and Since, so this keeps satisfying every field typed that way.
type readOnlyClock struct{ now time.Time }

func (c readOnlyClock) Now() time.Time                  { return c.now }
func (c readOnlyClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

// TestAHandWrittenTimedReachesEveryConfigThatTakesOne is the acceptance
// criterion in one place: the nine SDK configurations whose Clock field is
// typed clock.Timed each accept a time source built outside this module.
//
// Nine is measured, not remembered — the inventory script in ADR 0090 derives
// it by resolving every pkg/v1 alias to its internal type. A hand-written list
// had eight, missing lock.MemoryConfig.
//
// Before this package these fields were exported, visible in godoc, and
// assignable only to nil — so the domains behind them could not be tested
// deterministically by anyone downstream.
func TestAHandWrittenTimedReachesEveryConfigThatTakesOne(t *testing.T) {
	t.Parallel()

	//: one instance, assigned nine times: the property under test is that the
	//: SAME external value satisfies every field, not that nine types compile.
	hand := &handClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	//: each entry pairs a config's Clock field with the name a failure should
	//: report, so a future field rename names itself instead of a line number.
	assigned := map[string]clock.Timed{
		"scheduler.Config":     scheduler.Config{Clock: hand}.Clock,
		"lock.MemoryConfig":    lock.MemoryConfig{Clock: hand}.Clock,
		"lock.FileConfig":      lock.FileConfig{Clock: hand}.Clock,
		"lock.KeepaliveConfig": lock.KeepaliveConfig{Clock: hand}.Clock,
		"session.FileConfig":   session.FileConfig{Clock: hand}.Clock,
		"sql.Config":           sql.Config{Clock: hand}.Clock,
		"health.Config":        health.Config{Clock: hand}.Clock,
		"lifecycle.Config":     lifecycle.Config{Clock: hand}.Clock,
		"queue.ConsumerConfig": queue.ConsumerConfig{Clock: hand}.Clock,
	}

	if len(assigned) != 9 {
		t.Fatalf("expected the 9 Timed-bearing configs, got %d", len(assigned))
	}

	for name, got := range assigned {
		//: read the field back rather than trusting the literal: an alias that
		//: silently pointed at a different type would still compile if the
		//: field were dropped, and this catches that.
		if got != clock.Timed(hand) {
			t.Errorf("%s: Clock field does not hold the injected clock", name)
		}
	}
}

// TestATwoMethodClockReachesAConfigTypedClock covers the neighbouring case:
// a field typed clock.Clock takes a double with Now and Since alone. That
// already worked by structural typing — what did not is NAMING the type, which
// is what this assignment through clock.Clock now does.
func TestATwoMethodClockReachesAConfigTypedClock(t *testing.T) {
	t.Parallel()

	//: the conversion is the assertion — it compiles only because the two-method
	//: double satisfies the named public type.
	injected := clock.Clock(readOnlyClock{now: time.Unix(0, 0).UTC()})

	cfg := cache.Config[string, int]{MaxEntries: 8, Clock: injected}
	if cfg.Clock != injected {
		t.Fatal("cache.Config did not hold the injected clock")
	}
}

// TestManualClockDrivesASchedulerWithNoRealWait is the reason ManualClock is
// published. It runs a scheduler whose entry is due one hour out and observes
// the fire — with no time.Sleep, no polling and no wall-clock tolerance, in
// whatever time the goroutines take to hand off.
//
// BlockUntil is what makes it deterministic rather than merely fast: it waits
// until the engine has ARMED its wait, closing the race where the test would
// otherwise advance the clock before anybody was listening.
func TestManualClockDrivesASchedulerWithNoRealWait(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manual := clock.NewManualClock(start)

	//: buffered so the job never blocks on a receiver that has gone away; the
	//: assertion is that it ran, not that the test was ready for it.
	fired := make(chan time.Time, 1)

	sched := scheduler.New(scheduler.Config{Clock: manual})

	//: hourly, expressed directly as a Schedule so the test exercises the clock
	//: rather than the cron parser.
	hourly := func(after time.Time) (time.Time, bool) { return after.Add(time.Hour), true }

	if err := sched.Add(scheduler.Entry{
		Name:     "hourly",
		Schedule: hourly,
		Job: func(context.Context) error {
			fired <- manual.Now()
			return nil
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx) }()

	//: wait for the engine's timer to exist instead of guessing that it does.
	manual.BlockUntil(1)

	if got := manual.Pending(); got != 1 {
		t.Fatalf("expected exactly 1 armed wait after BlockUntil, got %d", got)
	}

	//: the hour passes at the speed of a function call.
	manual.Advance(time.Hour)

	at := <-fired
	if want := start.Add(time.Hour); !at.Equal(want) {
		t.Fatalf("job observed %s, want %s", at, want)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	//: the clock never left the instant the test put it at — proof that no real
	//: time was consumed to get here.
	if got := manual.Now(); !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("clock moved to %s on its own, want %s", got, start.Add(time.Hour))
	}
}
