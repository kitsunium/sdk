//go:build !race

package logger_test

import (
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// TestFanoutWidthAddsNoAllocation pins the half of the SDK's "one alloc per
// emit" claim that TestV116BuildSendAllocatesOnePerEmit never touched. That
// guard measures DEPTH — the handler's attrs clone on a single sink — and says
// nothing about WIDTH, so a fan-out that allocated once per record on every
// healthy write went unnoticed until a profile found it.
//
// The defect: multi.fanoutSink.Write opened its error slate with
// make([]error, 0, len(s.branches)). A capacity that is not a constant leaves
// the compiler's implicit stack budget at three branches, so every record cost
// a heap allocation — including the ordinary one where no branch fails and the
// slate stays empty — and the cost appeared only once a service happened to
// wire a third sink.
//
// The assertion is DIFFERENTIAL, against width 1 rather than a hardcoded
// count: it keeps biting when the baseline emit cost legitimately changes, and
// it fails on the one thing actually forbidden — a branch count visible in the
// allocation profile. multi.New does not short-circuit a single branch, so the
// baseline runs through the very same fanoutSink and the delta isolates the
// width and nothing else.
//
// Verified to bite rather than merely to pass: with the pre-sized slate
// restored, widths 3, 4 and 8 report one allocation more than width 1 while
// width 2 stays at the baseline — the three-branch escape threshold showing
// through, which is also why width 2 is kept in the table.
//
// Carries //go:build !race (testing.AllocsPerRun reports +1 under -race) and
// no t.Parallel (AllocsPerRun reads a process-global counter, so a concurrent
// sibling would corrupt the measurement). Run via
// `bazel test --config=pure //pkg/v1/logger:logger_test`, covered by the
// race-off alloc lane in tools/alloc-lane-targets.txt (rule 12).
// fanoutRuns is the iteration count both arms share. It is large enough that a
// per-record regression is unmistakable and small enough to stay fast.
const fanoutRuns int = 2000

// allocRuntimeWarmup is how many calls warmRuntimeCaches spends on a THROWAWAY
// Logger before either arm is measured, and it exists because a DIFFERENTIAL
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
// The THROWAWAY Logger is load-bearing. The cache is process-global and per call
// site, so anything can pay for it, while warming through the object under test
// would push an accumulating regression past its own growth steps — the same
// blindness this file counts totals to avoid.
const allocRuntimeWarmup int = 30000

// mallocsOver totals the allocations f performs across runs, and exists because
// testing.AllocsPerRun cannot see an amortised one. Its last line is
// `float64(mallocs / uint64(runs))` — an INTEGER division, documented in the
// stdlib as being there so a caller can write `== 1` instead of `< 2`. Any
// defect allocating less than once per call therefore reports exactly 0.0: six
// allocations across five hundred calls is "0". A slice that doubles is exactly
// such a defect, and the sibling guard in
// internal/service/writer/levelgate found one that way, with a mutation that
// PASSED against AllocsPerRun.
//
// A total is not subject to that rounding. The bookkeeping mirrors
// AllocsPerRun's otherwise — pin GOMAXPROCS so no other P allocates into the
// count, warm up so lazily-initialised state is not attributed to the loop, and
// read the counter either side. No runtime.GC(): an explicit collection returns
// before its sweep finishes, so the residual work allocates INSIDE the window.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: collection is held off for the window, because this assertion is
	//: DIFFERENTIAL and a collection is not. Every emit here draws its builder
	//: from a sync.Pool, and a collection DRAINS that pool, so the next few
	//: Build calls miss and cost about seven extra allocations — measured, in
	//: roughly one window in ten. Landing in the width-N arm and not in the
	//: width-1 baseline, that is a seven-allocation difference and a false
	//: failure; landing in both, it cancels. Neither is a measurement.
	//: The argument is evaluated now and the previous rate restored on return.
	//: NOT runtime.GC(), which returns before its sweep finishes and so
	//: allocates INSIDE the window it was meant to clear.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
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

// warmRuntimeCaches emits through a Logger nothing else will touch, so neither
// measured arm pays for the runtime's lazily-built caches.
func warmRuntimeCaches(t *testing.T) {
	t.Helper()
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:    logger.Multi(discardSink{}),
		Encoder: logger.TextEncoder(),
	})
	if err != nil {
		t.Fatalf("building the warm-up Logger: %v", err)
	}
	ctx := t.Context()
	//: the emit path is what both arms walk, so it is what is warmed.
	for range allocRuntimeWarmup {
		logger.Build(lg, logger.LevelInfo).Str("k1", "v1").Int("n1", 1).Send(ctx, "warm")
	}
}

func TestFanoutWidthAddsNoAllocation(t *testing.T) {
	ctx := t.Context()
	type tc struct {
		name  string
		width int
	}
	tests := []tc{
		{name: "two branches cost what one costs", width: 2},
		{name: "three branches cost what one costs", width: 3},
		{name: "four branches cost what one costs", width: 4},
		{name: "eight branches cost what one costs", width: 8},
	}
	//: emitAllocs wires a Logger fanning out to width discard sinks and reports
	//: what one Send costs on it.
	emitAllocs := func(t *testing.T, width int) uint64 {
		t.Helper()
		//: appended rather than indexed into a sized slice: branches is a slice
		//: of INTERFACES, so the zero value a fill would write is nil — and
		//: multi.New drops nil entries, which would silently measure a fan-out
		//: narrower than the one named by the case.
		branches := make([]logger.Sink, 0, width)
		//: every branch is the same do-nothing sink, so a per-branch allocation
		//: could only come from the fan-out itself.
		for range width {
			branches = append(branches, discardSink{})
		}
		lg, err := logger.NewWithSink(logger.SinkConfig{
			Sink:    logger.Multi(branches...),
			Encoder: logger.TextEncoder(),
		})
		if err != nil {
			t.Fatalf("NewWithSink(width=%d) err = %v", width, err)
		}
		//: warm the pool so the measured runs hit the recycled-builder path.
		logger.Build(lg, logger.LevelInfo).Str("warm", "up").Send(ctx, "warm")
		return mallocsOver(fanoutRuns, func() {
			b := logger.Build(lg, logger.LevelInfo).Str("k1", "v1").Int("n1", 1)
			allocSink = b
			b.Send(ctx, "msg")
		})
	}
	//: before either arm, and never through a Logger an arm measures.
	warmRuntimeCaches(t)
	base := emitAllocs(t, 1)
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := emitAllocs(t, tc.width)
		//: strictly equal, never "at most base+k": widening a fan-out must not
		//: show up in the allocation profile at all.
		if got != base {
			t.Errorf("%s: %d emits at width %d performed %d allocations, want %d (the width-1 baseline) — fan-out width must cost nothing", tc.name, fanoutRuns, tc.width, got, base)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
