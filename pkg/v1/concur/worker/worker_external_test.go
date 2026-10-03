package worker_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/concur/worker"
)

// tickWait bounds how long a test waits for something the code under test has
// already been told to do: it covers a goroutine being scheduled, never an
// interval elapsing on the wall clock.
const tickWait time.Duration = 5 * time.Second

// await fails the test unless ch delivers within tickWait.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(tickWait):
		t.Fatalf("%s did not happen", what)
	}
}

// TestStopSignalsTheLoopAndJoinsIt asserts the daemon's contract through both
// public constructors: the loop runs, Stop closes its stop channel and returns
// only once it has returned, Done is then closed, and a second Stop is a no-op.
func TestStopSignalsTheLoopAndJoinsIt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start func(worker.Loop) *worker.LoopDaemon
	}{
		{"Start", worker.Start},
		{"NewLoopDaemon", worker.NewLoopDaemon},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			running := make(chan struct{})
			var returned bool
			d := tc.start(func(stop <-chan struct{}) {
				close(running)
				<-stop
				returned = true
			})
			await(t, running, "the loop starting")
			d.Stop()
			if !returned {
				t.Error("Stop returned before the loop did")
			}
			await(t, d.Done(), "Done closing")
			d.Stop()
		})
	}
}

// TestEveryTicksOnTheInjectedClock asserts that Every ticks once per interval
// of a pkg/v1 ManualClock — no wall-clock wait — and that Stop releases the
// ticker, leaving nothing armed on the clock.
func TestEveryTicksOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fired := make(chan struct{}, 1)
	d := worker.Every(time.Minute, func() { fired <- struct{}{} }, worker.WithClock(mc))
	mc.BlockUntil(1) // the ticker is armed on the daemon's own goroutine
	mc.Advance(time.Minute)
	await(t, fired, "a tick after one interval")
	mc.Advance(time.Minute)
	await(t, fired, "a tick after the second interval")
	d.Stop()
	if pending := mc.Pending(); pending != 0 {
		t.Errorf("after Stop the clock holds %d armed waits, want 0", pending)
	}
}

// TestWithDoneEndsTheLoopWithoutStop asserts that closing the WithDone
// channel ends the loop on its own, and that Stop still joins at once.
func TestWithDoneEndsTheLoopWithoutStop(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	d := worker.Every(time.Hour, func() {}, worker.WithDone(done))
	close(done)
	await(t, d.Done(), "the loop leaving when its owner's work ended")
	d.Stop()
}

// TestTheMistakesPanicAtTheCall asserts that a nil loop, a nil tick and a
// non-positive interval panic at the call that passed them.
func TestTheMistakesPanicAtTheCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func()
	}{
		{"a nil loop", func() { worker.Start(nil) }},
		{"a nil tick", func() { worker.Every(time.Second, nil) }},
		{"a zero interval", func() { worker.Every(0, func() {}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", tc.name)
				}
			}()
			tc.call()
		})
	}
}

// ExampleEvery ticks a job on a manual clock: an hour passes in an instant,
// and the job runs once per interval it passes.
func ExampleEvery() {
	mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ran := make(chan time.Time)
	d := worker.Every(time.Hour, func() { ran <- mc.Now() }, worker.WithClock(mc))
	mc.BlockUntil(1) // wait for the ticker to be armed before moving time
	mc.Advance(time.Hour)
	fmt.Println((<-ran).Format(time.Kitchen))
	d.Stop()
	// Output: 1:00AM
}
