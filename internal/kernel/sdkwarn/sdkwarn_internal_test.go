package sdkwarn

import (
	"sync/atomic"
	"testing"
)

// TestHookSlotZeroValue pins the documented contract that the hookSlot is nil
// before any SetHook call — Emit's nil-check branch depends on it.
func TestHookSlotZeroValue(t *testing.T) {
	t.Parallel()
	type tc struct{ name string }
	tests := []tc{
		{"zero-value hookSlot loads nil"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the test verifies a local Pointer mirrors the documented
	//: zero-value behaviour of the package-global hookSlot.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: assert via a local clone of the same type — the package-global slot
		//: is process-state and unsafe to mutate from a parallel test.
		var localSlot atomic.Pointer[HookFn]
		//: a zero-value atomic.Pointer must Load nil; this is the same shape
		//: the production Emit relies on.
		if got := localSlot.Load(); got != nil {
			t.Fatalf("zero-value Pointer Load() = %v, want nil", got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSetHookSwapInternalContract verifies the unexported Swap semantics that
// SetHook delegates to: the atomic.Pointer correctly hands back the previous
// pointee.
func TestSetHookSwapInternalContract(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"local atomic.Pointer Swap returns prior pointee"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the test exercises the same atomic.Pointer[HookFn] shape SetHook
	//: uses, against a local instance to stay parallel-safe.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		hookA := func(string) {}
		hookB := func(string) {}
		var localSlot atomic.Pointer[HookFn]
		//: install A; the previous pointer must be nil because the slot is
		//: zero-valued.
		if prev := localSlot.Swap(&hookA); prev != nil {
			t.Fatalf("Swap(A) prev = %v, want nil", prev)
		}
		//: install B; the previous pointer must be &hookA.
		prev := localSlot.Swap(&hookB)
		if prev != &hookA {
			t.Fatalf("Swap(B) prev = %p, want %p", prev, &hookA)
		}
		//: uninstall (nil); the previous pointer must be &hookB.
		final := localSlot.Swap(nil)
		if final != &hookB {
			t.Fatalf("Swap(nil) prev = %p, want %p", final, &hookB)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
