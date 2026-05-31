package worker_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/worker"
)

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
