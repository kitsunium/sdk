package worker_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// tickWait bounds how long a test waits for a tick the clock has already
// delivered: generous, because it only has to cover a goroutine being
// scheduled, never a wall-clock interval elapsing.
const tickWait time.Duration = 5 * time.Second

// awaitTick fails the test unless fired delivers within tickWait.
func awaitTick(t *testing.T, fired <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-fired:
	case <-time.After(tickWait):
		t.Fatalf("%s: no tick", what)
	}
}

// TestEvery validates the ticker helper: it fires tick repeatedly and Stop
// both ends the ticking and joins the goroutine; nil tick / bad interval panic.
func TestEvery(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		runner func(t *testing.T)
	}
	tests := []tc{
		{
			name: "nil tick panics",
			runner: func(t *testing.T) {
				//: a nil tick is a programmer error — Every must fail loud.
				defer func() {
					//: the deferred recover turns the expected panic into a pass.
					if recover() == nil {
						t.Error("Every(_, nil) did not panic")
					}
				}()
				worker.Every(time.Millisecond, nil)
			},
		},
		{
			name: "non-positive interval panics",
			runner: func(t *testing.T) {
				//: time.NewTicker panics on a non-positive interval; Every
				//: surfaces the same contract at its own call site.
				defer func() {
					//: the deferred recover turns the expected panic into a pass.
					if recover() == nil {
						t.Error("Every(0, fn) did not panic")
					}
				}()
				worker.Every(0, func() {})
			},
		},
		{
			name: "ticks on the injected clock, once per interval it passes",
			runner: func(t *testing.T) {
				mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
				fired := make(chan struct{}, 1)
				d := worker.Every(time.Minute, func() { fired <- struct{}{} }, worker.WithClock(mc))
				defer d.Stop()
				//: the loop arms its ticker on its own goroutine; advancing
				//: before it has would be a lost wake, not a missing tick.
				mc.BlockUntil(1)
				//: a minute of manual time, no wall-clock time at all.
				mc.Advance(time.Minute)
				awaitTick(t, fired, "after one interval")
				mc.Advance(time.Minute)
				awaitTick(t, fired, "after the second interval")
				//: Stop releases the ticker, so the clock holds nothing armed.
				d.Stop()
				if pending := mc.Pending(); pending != 0 {
					t.Errorf("after Stop the clock holds %d armed waits, want 0", pending)
				}
			},
		},
		{
			name: "a nil clock and a nil option are the wall clock",
			runner: func(t *testing.T) {
				fired := make(chan struct{}, 1)
				d := worker.Every(time.Millisecond, func() {
					//: non-blocking: one pending tick is all the assertion needs.
					select {
					case fired <- struct{}{}:
					default:
					}
				}, worker.WithClock(nil), nil)
				defer d.Stop()
				awaitTick(t, fired, "on the wall clock")
			},
		},
		{
			name: "WithDone ends the loop without Stop",
			runner: func(t *testing.T) {
				done := make(chan struct{})
				d := worker.Every(time.Hour, func() {}, worker.WithDone(done))
				//: the owner's own end, before anybody calls Stop.
				close(done)
				select {
				case <-d.Done():
					//: the loop left on its own.
				case <-time.After(tickWait):
					t.Fatal("closing the WithDone channel did not end the loop")
				}
				//: Stop is still the join, and returns at once on a loop that left.
				d.Stop()
			},
		},
		{
			name: "ticks fire then Stop joins",
			runner: func(t *testing.T) {
				var ticks atomic.Int64
				d := worker.Every(time.Millisecond, func() { ticks.Add(1) })
				//: spin-wait for at least one tick so the ticker loop is proven live.
				deadline := time.Now().Add(2 * time.Second)
				for ticks.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if ticks.Load() == 0 {
					t.Fatal("Every never fired tick")
				}
				//: Stop ends ticking and joins; no further tick after Stop returns.
				d.Stop()
				settled := ticks.Load()
				time.Sleep(10 * time.Millisecond)
				if ticks.Load() != settled {
					t.Errorf("tick fired after Stop: %d, want %d", ticks.Load(), settled)
				}
			},
		},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; each runner owns its own recover where a panic is expected.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		tc.runner(t)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
