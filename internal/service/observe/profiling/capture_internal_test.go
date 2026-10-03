// Package profiling — the CPU window measured on an injected clock. Not
// parallel, like every CPU capture test: the process has one CPU profiler.
package profiling

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Test_captureCPU pins that the CPU window is measured on the clock it is
// given: a full five-minute window — MaxCPUWindow — ends when a manual clock is
// advanced past it, and not a nanosecond before, so the profiler is held for
// the test's own scheduling rather than for five minutes of wall time.
//
// GOROUTINE LIFECYCLE: one goroutine runs the capture and reports on a
// buffered channel; the test always receives from it before returning.
func Test_captureCPU(t *testing.T) {
	type tc struct {
		name   string
		window time.Duration
	}
	tests := []tc{
		{"the longest window the package allows", MaxCPUWindow},
		{"a one-second window", time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
		type outcome struct {
			profile *ProfileValue
			err     error
		}
		done := make(chan outcome, 1)
		go func() {
			p, err := captureCPU(t.Context(), manual, c.window)
			done <- outcome{p, err}
		}()
		//: the profiler is running and the window's timer is armed.
		manual.BlockUntil(1)
		manual.Advance(c.window - time.Nanosecond)
		select {
		case got := <-done:
			t.Fatalf("the capture returned (%v) before its window closed", got.err)
		default:
		}
		manual.Advance(time.Nanosecond)
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("captureCPU = %v, want a profile", got.err)
			}
			if got.profile == nil || got.profile.PeriodType.Type != "cpu" {
				t.Errorf("captureCPU returned %+v, want a CPU profile", got.profile)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("advancing the clock past the window did not end the capture")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
