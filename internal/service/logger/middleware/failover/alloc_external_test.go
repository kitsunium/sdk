//go:build !race

package failover_test

import (
	"context"
	"runtime"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/failover"
)

// failoverRuns is the iteration count both arms share.
const failoverRuns int = 2000

// allocSink defeats dead-code elimination in the allocation probe.
var allocSink int

// TestChainDepthAddsNoAllocationOnTheHealthyPath pins the property the failover
// contract is actually sold on: a fallback chain costs nothing until it is
// used. The happy path returns on the FIRST branch and never appends to the
// error slate — yet that slate was opened with
// make([]error, 0, len(s.chain)), and a capacity that is not a constant leaves
// the compiler's implicit stack budget at three branches. Every write on a
// three-deep chain therefore paid a heap allocation for failures that had not
// happened, and the price appeared only once an operator added a third
// fallback: exactly the configuration where the chain is least likely to be
// exercised and most likely to be long.
//
// The assertion is DIFFERENTIAL, against a one-branch chain rather than a
// hardcoded count, so it survives a legitimate change in the baseline write
// cost and fails only on the forbidden thing — a chain depth visible in the
// allocation profile of a write that never falls over. Verified to bite: with
// the pre-sized slate restored, depths 3, 4 and 8 report one allocation more
// than depth 1 while depth 2 stays at the baseline, which is the three-branch
// escape threshold showing through.
//
// Carries //go:build !race (testing.AllocsPerRun reports +1 under -race) and
// no t.Parallel (AllocsPerRun reads a process-global counter). It runs in
// exactly one lane — `make test-alloc`, via the entry this commit adds to
// tools/alloc-lane-targets.txt (rule 12).
// mallocsOver totals the allocations f performs across runs, because
// testing.AllocsPerRun cannot see an amortised one: its last line is
// `float64(mallocs / uint64(runs))`, an INTEGER division the stdlib documents
// as being there so a caller can write `== 1` instead of `< 2`. Anything
// allocating less than once per call reports exactly 0.0 — measured, an
// `append` to a doubling slice over 2 000 calls reports "0". A total does not
// round: the sibling guard in pkg/v1/logger catches that same shape at 2 002
// against 2 012.
//
// The bookkeeping mirrors AllocsPerRun's otherwise — pin GOMAXPROCS so no other
// P allocates into the count, warm up so lazily-initialised state is not
// attributed to the loop, read the counter either side. Deliberately no
// runtime.GC(): a collection returns before its sweep finishes, so the residual
// work allocates INSIDE the window.
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

func TestChainDepthAddsNoAllocationOnTheHealthyPath(t *testing.T) {
	ctx := context.Background()
	payload := []byte("a log line that no branch will refuse\n")
	type tc struct {
		name  string
		depth int
	}
	tests := []tc{
		{name: "two branches cost what one costs", depth: 2},
		{name: "three branches cost what one costs", depth: 3},
		{name: "four branches cost what one costs", depth: 4},
		{name: "eight branches cost what one costs", depth: 8},
	}
	//: writeAllocs builds a depth-deep chain whose FIRST branch always accepts
	//: and reports what one write costs on it.
	writeAllocs := func(t *testing.T, depth int) uint64 {
		t.Helper()
		//: appended rather than indexed into a sized slice: branches is a slice
		//: of INTERFACES, so the zero value a fill would write is nil — and
		//: failover.New drops nil entries, which would silently measure a chain
		//: shorter than the one named by the case.
		branches := make([]corelogger.Sink, 0, depth)
		//: every branch accepts, so the write returns on the first one and the
		//: deeper branches exist without ever being reached — the shape an
		//: operator actually runs in.
		for range depth {
			branches = append(branches, &controlledSink{})
		}
		sink, err := failover.New(branches...)
		if err != nil {
			t.Fatalf("New(depth=%d) err = %v", depth, err)
		}
		return mallocsOver(failoverRuns, func() {
			n, werr := sink.Write(ctx, corelogger.RecordEvent{}, payload)
			//: read both results so the call cannot be optimised away.
			if werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			allocSink = n
		})
	}
	base := writeAllocs(t, 1)
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := writeAllocs(t, tc.depth)
		//: strictly equal, never "at most base+k": an unused fallback must not
		//: show up in the allocation profile at all.
		if got != base {
			t.Errorf("%s: %d writes at depth %d performed %d allocations, want %d (the depth-1 baseline) — an unexercised fallback must cost nothing", tc.name, failoverRuns, tc.depth, got, base)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
