// Package reaper — black-box facade tests: alias identity, no-op-safe lifecycle,
// and the typed-error contract of SetChildSubreaper. Platform-specific
// assertions live in the build-tagged _unix / _nonunix siblings so they track
// the same unix / !unix split as the implementation, not a runtime.GOOS guess.
package reaper_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/reaper"
)

// TestNewLifecycleIsSafe asserts the facade's New/Start/Stop/ReapOnce surface is
// usable and safe on every platform: ReapOnce never errors on an idle process,
// and a Start/Stop cycle completes without panic.
func TestNewLifecycleIsSafe(t *testing.T) {
	//: safe to run alongside the rest of the package: nothing here spawns a
	//: child, so the sweeps below have nothing to steal from one another. The
	//: sub-cases stay serial because each drives one reaper through an ordered
	//: Start/Stop sequence.
	t.Parallel()
	type tc struct {
		name  string
		steps func(t *testing.T, r reaper.Reaper)
	}
	tests := []tc{
		{"Stop before Start is a no-op", func(t *testing.T, r reaper.Reaper) {
			t.Helper()
			r.Stop()
		}},
		{"Start is idempotent", func(t *testing.T, r reaper.Reaper) {
			t.Helper()
			r.Start()
			//: a second Start must not raise a second loop.
			r.Start()
			r.Stop()
		}},
		{"an idle sweep reaps nothing and errors not at all", func(t *testing.T, r reaper.Reaper) {
			t.Helper()
			r.Start()
			got, err := r.ReapOnce()
			if err != nil {
				t.Fatalf("ReapOnce returned error: %v", err)
			}
			if got < 0 {
				t.Fatalf("ReapOnce = %d, want >= 0", got)
			}
			r.Stop()
		}},
		{"Stop is idempotent", func(t *testing.T, r reaper.Reaper) {
			t.Helper()
			r.Start()
			r.Stop()
			//: a second Stop must drain nothing and not panic.
			r.Stop()
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		c.steps(t, reaper.New())
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			//: each sub-case drives its own reaper, so they do not contend.
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestWithOnReapObserves asserts the WithOnReap option is accepted and the
// post-sweep observer fires on every platform — including the non-Unix no-op,
// where the documented contract still calls the hook with a zero-child sweep.
func TestWithOnReapObserves(t *testing.T) {
	//: see TestNewLifecycleIsSafe: the counter below is fed only by this test's
	//: own synchronous sweeps, never by a background loop.
	t.Parallel()
	type tc struct {
		name   string
		sweeps int
	}
	tests := []tc{
		//: the contract is platform-consistent: the hook fires on EVERY sweep
		//: including a zero-child one, on Unix and on the non-Unix no-op alike.
		{"one sweep fires the hook once", 1},
		{"each sweep fires it again", 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		calls := 0
		r := reaper.New(reaper.WithOnReap(func(int) { calls++ }))
		for range c.sweeps {
			if _, err := r.ReapOnce(); err != nil {
				t.Fatalf("ReapOnce returned error: %v", err)
			}
		}
		if calls != c.sweeps {
			t.Errorf("observer fired %d times for %d sweeps", calls, c.sweeps)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			//: each sub-case drives its own reaper, so they do not contend.
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestIsPID1 asserts IsPID1 reports a bool consistent with the running pid; the
// test process is virtually never pid 1, so it must report false here.
func TestIsPID1(t *testing.T) {
	t.Parallel()
	//: the test runner is not the init process, so IsPID1 must be false.
	if reaper.IsPID1() {
		t.Fatalf("IsPID1 = true, but the test process is not pid 1")
	}
}
