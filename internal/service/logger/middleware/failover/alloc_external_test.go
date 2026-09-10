//go:build !race

package failover_test

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/failover"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probe.
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
	writeAllocs := func(t *testing.T, depth int) float64 {
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
		return testing.AllocsPerRun(2000, func() {
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
			t.Errorf("%s: allocs/op at depth %d = %v, want %v (the depth-1 baseline) — an unexercised fallback must cost nothing", tc.name, tc.depth, got, base)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
