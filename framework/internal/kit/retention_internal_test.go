package kit

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// countedClock is a manual clock that keeps the timers armed on it — what
// each was armed for, so that a test tells the timer a loop sleeps on from
// one it drops (loop_internal_test.go).
type countedClock struct {
	*clock.ManualClock
	armed []time.Duration
}

func (c *countedClock) NewTimer(d time.Duration) clock.Timer {
	c.armed = append(c.armed, d)
	return c.ManualClock.NewTimer(d)
}

// A retention's wait takes a wake already there before it arms a timer: an
// armed timer is one the loop sleeps on. A run's own erasure leaves such a
// wake; taken after the timer, it made the loop drop that timer and arm
// another — and a test that waits for the loop's timer before it moves the
// clock could move it in between, before the second read the clock.
func TestARetentionTakesWhatCameBeforeItArmsATimer(t *testing.T) {
	clk := &countedClock{ManualClock: clock.NewManualClock(time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC))}
	due := clk.Now().Add(time.Hour)
	r := &retentionRun[struct{}]{
		a: &App{clock: clk}, agenda: map[string]time.Time{"k": due},
		wake: make(chan struct{}, 1),
	}
	r.signal()
	if reason, run, ok := r.wait(t.Context(), due, time.Time{}); reason != "" || run || !ok {
		t.Errorf("a write that makes nothing due: %q, run %v, ok %v", reason, run, ok)
	}
	if len(clk.armed) != 0 {
		t.Errorf("timers armed for %v, then dropped, for wakes already there", clk.armed)
	}
}
