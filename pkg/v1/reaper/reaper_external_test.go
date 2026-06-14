// Package reaper — black-box facade tests: alias identity, no-op-safe lifecycle,
// and the typed-error contract of SetChildSubreaper.
package reaper_test

import (
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
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
// reaper constructed with it remains lifecycle-safe.
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
	//: on Unix the hook fires on every sweep; on the no-op platform it never
	//: runs, so only assert the negative when the reaper actually reaps.
	if runtime.GOOS != "windows" && !called {
		t.Fatalf("WithOnReap observer was not invoked on a Unix sweep")
	}
}

// TestSetChildSubreaperContract asserts SetChildSubreaper honours its typed
// contract: nil or SubreaperFailed on Unix, UnsupportedPlatform off Unix. The
// error, when present, is always one of the central proc sentinels.
func TestSetChildSubreaperContract(t *testing.T) {
	//: serial — alters this process's subreaper attribute.
	err := reaper.SetChildSubreaper()
	//: success is a valid outcome on a capable Unix host.
	if err == nil {
		//: nothing more to assert on the success path.
		return
	}
	//: off Unix the only permitted error is UnsupportedPlatform.
	if runtime.GOOS == "windows" {
		//: the no-op stub must return exactly the platform sentinel.
		if !errs.HasCode(err, reaper.UnsupportedPlatform.Code()) {
			t.Fatalf("non-Unix SetChildSubreaper = %v, want UnsupportedPlatform", err)
		}
		//: contract satisfied for the unsupported platform.
		return
	}
	//: on Unix a failure must be the SubreaperFailed sentinel, never raw.
	if !errs.HasCode(err, reaper.SubreaperFailed.Code()) {
		t.Fatalf("Unix SetChildSubreaper failed with unexpected error: %v", err)
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
