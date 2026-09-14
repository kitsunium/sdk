// Package clock_test proves the one property this facade exists for: a time
// source written OUTSIDE internal/ can be named, built, and handed to every
// SDK configuration that takes one.
//
// Every declaration below is written against pkg/v1 alone. Nothing here
// imports internal/kernel/clock, because the consumer this package was added
// for cannot — and a test that reached for the internal package would prove
// the opposite of what it claims (the precedent is pkg/v1/token, whose facade
// test hid two constructors no downstream module could call).
package clock_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/cache"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/health"
	"github.com/kitsunium/sdk/pkg/v1/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/lock"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/scheduler"
	"github.com/kitsunium/sdk/pkg/v1/session"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

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

// Compile-time assertions that the two doubles satisfy the published port.
// These are the whole point of the package, so they fail the build rather than
// a test run.
var (
	_ clock.Clock  = (*handClock)(nil)
	_ clock.Waiter = (*handClock)(nil)
	_ clock.Timed  = (*handClock)(nil)
	_ clock.Clock  = readOnlyClock{}
	_ clock.Timer  = (*handTimer)(nil)
	_ clock.Ticker = (*handTicker)(nil)

	//: the production value satisfies both halves of the port, and naming the
	//: types here is itself the thing this package exists to make possible.
	_ clock.Timed  = clock.System
	_ clock.Clock  = clock.System
	_ clock.Waiter = clock.System
)

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

// TestSystemIsReferenceableAndIsATimed pins the production value's presence and
// its type. It is what every Clock field falls back to when left nil, so a
// consumer naming it explicitly beside a test double must be able to.
func TestSystemIsReferenceableAndIsATimed(t *testing.T) {
	t.Parallel()

	//: System is DECLARED clock.Timed — typed as the union rather than the
	//: reading half so a caller that needs to wait does not type-assert — so
	//: inference already yields the port type and no conversion is written. The
	//: package-level block pins the three assignabilities at compile time; this
	//: asserts the value itself is usable.
	timed := clock.System
	if timed == nil {
		t.Fatal("clock.System is nil")
	}

	//: reading through the frozen two-method half, which is what most SDK
	//: configurations ask for.
	reading := clock.Clock(clock.System)
	if reading.Since(reading.Now()) < 0 {
		t.Fatal("clock.System reported a negative elapsed duration")
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
