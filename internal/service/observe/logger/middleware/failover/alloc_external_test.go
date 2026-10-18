//go:build !race

package failover_test

import (
	"context"
	"fmt"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/failover"
)

// failoverRuns is the iteration count both arms share.
const failoverRuns int = 2000

// allocRuntimeWarmup is how many calls warmRuntimeCaches spends on a THROWAWAY
// chain before either arm is measured, and it exists because a DIFFERENTIAL
// assertion is uniquely fragile to a stray allocation: one landing in either arm
// fails the comparison, and it fails it in whichever direction the stray fell.
//
// This guard did exactly that under Bazel, 1 run in 15: the DEPTH-1 BASELINE
// caught the stray and the measured arms did not, so it reported "depth 4
// performed 0 allocations, want 1" — the tested arms cleaner than the reference,
// which is nonsense as a regression signal.
//
// The cause is the runtime, not this package. runtime/iface.go builds its
// per-call-site type-switch cache LAZILY, gated behind `cheaprand()&1023 != 0`,
// so about one miss in 1024 pays for it and buildInterfaceSwitchCache allocates.
// It is invisible to testing.AllocsPerRun, which integer-divides it to 0.0, and
// it is exactly what a total-counting window sees. Thirty thousand calls put the
// probability the cache is still unbuilt at (1023/1024)^30000, about 2e-13.
//
// The THROWAWAY chain is load-bearing. The cache is process-global and per call
// site, so anything can pay for it, while warming through the object under test
// would push an accumulating regression past its own growth steps — the same
// blindness this file counts totals to avoid.
const allocRuntimeWarmup int = 30000

// allocRounds is how many rounds each arm is measured over, its fewest kept.
//
// The warm-up above did not close the comparison's fragility: on main at
// 96dfb083 (issue #270) the DEPTH-1 BASELINE still caught one allocation and
// every deeper arm caught none, so "depth 2 performed 0 allocations, want 1"
// failed the run. Whatever the runtime allocates once lands in ONE window, so
// the least of a few windows does not carry it; a cost of the chain is paid on
// every write and lands in every window, 2 000 allocations each, so the least
// still carries it whole. And the comparison is "at most the baseline": the
// property is that depth adds nothing, and an arm cleaner than its reference
// says nothing about the chain.
const allocRounds int = 3

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
// allocation profile of a write that never falls over. Each arm is the fewest
// of allocRounds windows, and a deeper arm may cost less than the baseline,
// never more. Verified to bite: with
// the pre-sized slate restored, depths 3, 4 and 8 report one allocation more
// than depth 1 while depth 2 stays at the baseline, which is the three-branch
// escape threshold showing through.
//
// Carries //go:build !race (the race detector allocates on its own) and no
// t.Parallel (mallocsOver sets the process's profile rate, collector and
// GOMAXPROCS for its window). It runs in
// exactly one lane — `make test-alloc`, via the entry this commit adds to
// tools/alloc-lane-targets.txt (rule 12).
// warmRuntimeCaches drives a chain nothing else will touch, so neither measured
// arm pays for the runtime's lazily-built caches.
func warmRuntimeCaches(t *testing.T, payload []byte) {
	t.Helper()
	sink, err := failover.New(&controlledSink{})
	if err != nil {
		t.Fatalf("building the warm-up chain: %v", err)
	}
	ctx := context.Background()
	//: the write path is what both arms walk, so it is what is warmed.
	for range allocRuntimeWarmup {
		if _, werr := sink.Write(ctx, corelogger.RecordEvent{}, payload); werr != nil {
			t.Fatalf("warming the runtime caches: %v", werr)
		}
	}
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
		got := mallocsOver(failoverRuns, func() {
			n, werr := sink.Write(ctx, corelogger.RecordEvent{}, payload)
			//: read both results so the call cannot be optimised away.
			if werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			allocSink = n
		})
		reportWindow(t, fmt.Sprintf("writes at depth %d", depth), got)
		return got.own
	}
	//: fewestAllocs is the least writeAllocs reports over allocRounds rounds,
	//: each on a chain of its own: a stray allocation the runtime makes once
	//: lands in one round, while a cost of the chain lands in every one.
	fewestAllocs := func(t *testing.T, depth int) uint64 {
		t.Helper()
		fewest := writeAllocs(t, depth)
		for range allocRounds - 1 {
			fewest = min(fewest, writeAllocs(t, depth))
		}
		return fewest
	}
	//: before either arm, and never through a chain an arm measures.
	warmRuntimeCaches(t, payload)
	base := fewestAllocs(t, 1)
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := fewestAllocs(t, tc.depth)
		//: never "at most base+k": an unused fallback must not show up in the
		//: allocation profile at all. And never "equal" either: a deeper
		//: chain allocating LESS than the baseline is a stray the baseline
		//: caught, not a regression (issue #270).
		if got > base {
			t.Errorf("%s: %d writes at depth %d performed %d allocations, want at most %d (the depth-1 baseline) — an unexercised fallback must cost nothing", tc.name, failoverRuns, tc.depth, got, base)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
