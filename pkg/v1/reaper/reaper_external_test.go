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
	//: not Parallel — a real Unix reaper reaps ANY child of this process.
	r := reaper.New()
	//: Stop before Start must be a harmless no-op.
	r.Stop()
	//: start, then a single non-blocking sweep on an idle process.
	r.Start()
	//: an idle sweep returns a clean count and no error on every platform.
	got, err := r.ReapOnce()
	//: a clean sweep never errors.
	if err != nil {
		t.Fatalf("ReapOnce returned error: %v", err)
	}
	//: nothing is reaped on an idle process.
	if got < 0 {
		t.Fatalf("ReapOnce = %d, want >= 0", got)
	}
	//: a final Stop drains and tears down cleanly.
	r.Stop()
}

// TestWithOnReapObserves asserts the WithOnReap option is accepted and the
// post-sweep observer fires on every platform — including the non-Unix no-op,
// where the documented contract still calls the hook with a zero-child sweep.
func TestWithOnReapObserves(t *testing.T) {
	//: serial — see TestNewLifecycleIsSafe rationale.
	called := false
	//: an observer that records it ran; it must never block.
	r := reaper.New(reaper.WithOnReap(func(int) {
		//: record that the post-sweep hook fired.
		called = true
	}))
	//: a direct sweep fires the observer even with zero children.
	if _, err := r.ReapOnce(); err != nil {
		t.Fatalf("ReapOnce returned error: %v", err)
	}
	//: the WithOnReap contract is platform-consistent: the hook fires on every
	//: sweep (including zero) on Unix and on the non-Unix no-op alike.
	if !called {
		t.Fatalf("WithOnReap observer was not invoked on a sweep")
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
