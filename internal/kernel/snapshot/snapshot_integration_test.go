//go:build !race

package snapshot_test

import (
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// allocRuns is how many times each claim is exercised inside the measured
// window. It is large enough that a buffer doubling on a growth step has grown
// several times over by the end, so the total is unmistakably non-zero.
const allocRuns int = 1000

// allocSink defeats dead-code elimination in the allocation probes.
var allocSink any

// mallocsOver reports the TOTAL heap allocations f performs across runs calls,
// rather than the per-call average testing.AllocsPerRun reports.
//
// The distinction is what this container needs, and nothing else in it would
// have shown the difference. Load returns a pointer and Store publishes one, so
// there is no per-call allocation for a regression to hide behind: any defect
// here is one that ACCUMULATES — a published-pointer history, an audit trail of
// writers, a debug ring of the last N snapshots. Every one of those is an
// append to a doubling slice, which allocates on the growth steps and not on
// the calls in between, and AllocsPerRun's last line is
// `float64(mallocs / uint64(runs))` — INTEGER division, documented in the
// stdlib as being there so a caller can write `== 1` instead of `< 2`. Eleven
// allocations across a thousand Stores is "0" to it.
//
// A total is not subject to that rounding. The bookkeeping mirrors
// AllocsPerRun's otherwise — pin GOMAXPROCS so no other P allocates into the
// count, warm up so lazily-initialised state is not attributed to the loop, and
// read the counter either side. There is deliberately NO runtime.GC(): an
// explicit collection returns before its sweep finishes, so the residual work
// allocates INSIDE the window; AllocsPerRun does not call it either, for the
// same reason.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	return after.Mallocs - before.Mallocs
}

// TestZeroAllocInvariant pins the documented zero-alloc steady-state hot paths
// for the snapshot container: Load, Store, and Swap of a pre-built pointer
// must allocate nothing per op (only the writer's own clone allocates, and
// that is the caller's, not the container's).
//
// MUTATION-CHECKED. Giving Value[T] a `published []*T` field and appending
// `next` to it inside Store — the first thing anyone reaches for when asked
// which snapshots a container has served — fails it at
// `Store: 1000 calls performed 10 allocations, want 0`. Ten, not a thousand:
// the slice doubles, so the allocation happens on the growth steps and not on
// the calls between them. testing.AllocsPerRun measured that same mutated
// Store, three separate runs, and reported 0 every time — which is why this
// file counts a TOTAL. See mallocsOver.
//
// Carries //go:build !race (the race detector allocates shadow state on every
// memory access, so any malloc count under -race measures the detector) and no
// t.Parallel (the malloc counter is process-global, so a concurrent sibling
// would corrupt the measurement). Run via
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
		//: steady-state hot path must allocate nothing at all, not "nothing
		//: on average" — an amortised regression averages to zero.
		if got := mallocsOver(allocRuns, tc.fn); got != 0 {
			t.Errorf("%s: %d calls performed %d allocations, want 0", tc.name, allocRuns, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
