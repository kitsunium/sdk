//go:build !race

package sdkwarn_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/sdkwarn"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probe.
var allocSink string

// TestAllocBudget pins the documented hook-dispatch allocation budget. Emit
// with a hook installed allocates exactly 1 per op (the fmt.Sprintf result
// string). The gate catches accidental extra allocations from future
// refactors (incidental string concat, slice copy, boxing).
//
// The gate is <= 1.0, not == 0: building the formatted string is the caller's
// intent and must allocate. The gate's value is rejecting regressions that
// introduce a *second* allocation (e.g. an internal []byte slab built
// incidentally by a future Emit body).
func TestAllocBudget(t *testing.T) {
	type tc struct {
		name   string
		format string
		args   []any
		want   float64
	}
	tests := []tc{
		{"single format arg", "budget %d", []any{42}, 1.0},
		{"no args", "plain warning", nil, 1.0},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; AllocsPerRun measures the hook-installed path which must
	//: allocate only the fmt.Sprintf result.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		prev := sdkwarn.SetHook(func(s string) { allocSink = s })
		t.Cleanup(func() { sdkwarn.SetHook(prev) })
		fn := func() { sdkwarn.Emit(tc.format, tc.args...) }
		//: warm-up call to amortise per-process init costs of the underlying
		//: log.Logger; the gate measures steady-state.
		fn()
		got := testing.AllocsPerRun(1000, fn)
		if got > tc.want {
			t.Errorf("Emit allocs = %.1f, want <= %.1f", got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc)
		})
	}
}
