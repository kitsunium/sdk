package kit

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// A declared loop's wait takes a wake already there before it arms a timer:
// an armed timer is one the loop sleeps on. A publish or a nudge that came
// while the loop ran made it arm its timer, then drop it at once for that
// wake — and, during a backoff, arm another, the backoff's end: a test that
// waits for the loop's timer before it moves the clock could move it in
// between, before the second read the clock.
func TestALoopTakesWhatCameBeforeItArmsATimer(t *testing.T) {
	clk := &countedClock{ManualClock: clock.NewManualClock(time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC))}
	now := clk.Now()
	r := &loopRun{a: &App{clock: clk}, wake: make(chan struct{}, 1)}
	l := &Loop{running: r}
	hourly := nextWake{at: now.Add(time.Hour), reason: model.WakeInterval}
	failed := nextWake{at: now.Add(time.Hour), reason: model.WakeInterval, floor: now.Add(time.Second)}
	joined := WakeEvent{Reason: model.WakeTopic, Topic: "members/topic/joined"}

	l.signal(joined)
	if w, ok := l.wait(t.Context(), r, hourly); w.Reason != model.WakeTopic || !ok {
		t.Errorf("a publish: %+v, ok %v", w, ok)
	}
	l.Nudge()
	if w, ok := l.wait(t.Context(), r, failed); w.Reason != model.WakeManual || !ok {
		t.Errorf("a nudge during a backoff: %+v, ok %v", w, ok)
	}
	if len(clk.armed) != 0 {
		t.Errorf("timers armed for %v, then dropped, for wakes already there", clk.armed)
	}

	// A publish during a backoff is held until its end: the one timer the
	// loop arms. Its context already done, the wait returns once that timer
	// is armed, rather than sleep on it until a clock nobody moves fires it.
	l.signal(joined)
	stopping, stop := context.WithCancel(t.Context())
	stop()
	if _, ok := l.wait(stopping, r, failed); ok {
		t.Error("a stopping loop woke")
	}
	if !slices.Equal(clk.armed, []time.Duration{time.Second}) {
		t.Errorf("timers armed for %v, want one, for the backoff's second", clk.armed)
	}
}
