//go:build !race

package snapshot_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes.
var allocSink any

// TestZeroAllocInvariant pins the documented zero-alloc steady-state hot paths
// for the snapshot container: Load, Store, and Swap of a pre-built pointer
// must allocate nothing per op (only the writer's own clone allocates, and
// that is the caller's, not the container's). Carries //go:build !race
// (testing.AllocsPerRun reports +1 under -race) and no t.Parallel (AllocsPerRun
// reads a process-global counter, so concurrent allocations from sibling
// subtests would corrupt the measurement). Run via
// `bazel test --config=alloc //internal/kernel/snapshot:snapshot_test` or
// `--config=pure`.
func TestZeroAllocInvariant(t *testing.T) {
	a, b := 1, 2
	v := snapshot.NewValue(&a)
	type tc struct {
		name string
		fn   func()
	}
	tests := []tc{
		{"Load", func() { allocSink = v.Load() }},
		{"Store", func() { v.Store(&b) }},
		{"Swap", func() { allocSink = v.Swap(&a) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: warm the path once so the measured runs hit steady state.
		tc.fn()
		//: steady-state hot path must allocate nothing per op.
		if got := testing.AllocsPerRun(1000, tc.fn); got != 0 {
			t.Errorf("%s: %.1f allocs/op, want 0", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
