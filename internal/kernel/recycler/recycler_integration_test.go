//go:build !race

package recycler_test

import (
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/buffer"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// allocRuns is how many round trips each claim performs inside the measured
// window. A pool instrumented with a growing slice has doubled it ten times
// over by the end, so the total is unmistakably non-zero.
const allocRuns int = 1000

// allocSink defeats dead-code elimination in the allocation probes.
var allocSink any

// mallocsOver reports the TOTAL heap allocations f performs across runs calls,
// rather than the per-call average testing.AllocsPerRun reports.
//
// The reason is specific to a pool. Get and Put are the two verbs everyone
// wants to COUNT — how many buffers are outstanding, which sizes are being
// discarded, who forgot to return one — and every such instrument is a slice
// that grows: `p.outstanding = append(p.outstanding, v)`. A slice that doubles
// allocates on the growth steps and not on the calls in between, and
// AllocsPerRun's last line is `float64(mallocs / uint64(runs))`, an INTEGER
// division documented in the stdlib as being there so a caller can write
// `== 1` instead of `< 2`. Ten allocations across a thousand round trips is
// "0" to it — measured, not inferred.
//
// A total is not subject to that rounding. The bookkeeping mirrors
// AllocsPerRun's otherwise — pin GOMAXPROCS so no other P allocates into the
// count, warm the per-P cache so the factory is not attributed to the loop, and
// read the counter either side. There is deliberately NO runtime.GC(): an
// explicit collection returns before its sweep finishes, so the residual work
// allocates INSIDE the window; AllocsPerRun does not call it either, for the
// same reason. It matters more here than anywhere — a GC also drains
// sync.Pool's victim cache, which would turn the very next Get into a factory
// call and measure the cold path this test exists to stay off.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: warm the per-P pool so the measured runs hit the cache path.
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
// for the recycler primitives and the byte-pool specialisation built on them.
//
// MUTATION-CHECKED. Giving Pool[T] a `returned []T` field and appending v to it
// inside Put — the shape of every "how many buffers are in flight?" instrument
// anyone would add — fails all three arms at once, because CappedPool and
// buffer both delegate their Put here:
//
//	Pool Get+Put: 1000 calls performed 10 allocations, want 0
//	CappedPool Get+Put: 1000 calls performed 10 allocations, want 0
//	buffer Get+Put: 1000 calls performed 10 allocations, want 0
//
// Ten, not a thousand: the slice doubles. testing.AllocsPerRun measured the
// same three mutated round trips, three separate runs, and reported 0 every
// time — so the old form of this test passed against a pool that really was
// allocating on the path it exists to keep free. See mallocsOver.
//
// Carries //go:build !race (the race detector allocates shadow state on every
// memory access, so any malloc count under -race measures the detector) and no
// t.Parallel (the malloc counter is process-global, so a concurrent sibling
// would corrupt the measurement). Run via
// `bazel test --config=alloc //internal/kernel/recycler:recycler_test` or
// `--config=pure`.
func TestZeroAllocInvariant(t *testing.T) {
	rec := recycler.NewPool[*[64]byte](func() *[64]byte { return &[64]byte{} })
	capped := recycler.NewCappedPool[*[]byte](
		func() *[]byte { return new(make([]byte, 0, 1024)) },
		func(b *[]byte) { *b = (*b)[:0] },
		func(b *[]byte) int { return cap(*b) },
		64<<10,
	)
	type tc struct {
		name string
		fn   func()
	}
	tests := []tc{
		{"Pool Get+Put", func() { v := rec.Get(); allocSink = v; rec.Put(v) }},
		{"CappedPool Get+Put", func() { v := capped.Get(); allocSink = v; capped.Put(v) }},
		{"buffer Get+Put", func() { b := buffer.Get(); allocSink = b; buffer.Put(b) }},
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
