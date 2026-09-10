//go:build !race

package logger_test

import (
	"context"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// traceRuns is how many emits each arm performs inside the measured window. A
// per-emit cost shows up as traceRuns allocations — the number the first
// assertion below is written against — and an amortised one as roughly its
// base-two logarithm, which is what the old form of these assertions could not
// see at all.
const traceRuns int = 2000

// traceAllocSink defeats dead-code elimination in the extraction probe. It is
// TYPED rather than an any: assigning a 24-byte struct into an interface boxes
// it, which is one heap allocation of the test's own making and would hide the
// very number this file measures.
var traceAllocSink logger.TraceContext

// allocProbeAttrs is pre-built outside the measured closure so the variadic
// slice is not re-allocated per call — the emit paths below then differ only
// in whether a span is in scope.
var allocProbeAttrs = []logger.Attr{logger.String("k", "v")}

// traceMallocsOver reports the TOTAL heap allocations f performs across runs
// calls, holding the garbage collector off for the duration of the window.
//
// It totals rather than averaging for the reason mallocsOver in
// fanout_integration_test.go gives: testing.AllocsPerRun ends in
// `float64(mallocs / uint64(runs))`, an INTEGER division, so every count from
// exactly one allocation per call up to just under two reports 1.0 and anything
// under one reports 0.0.
//
// It is a SECOND helper rather than a call to that one because of the GC, and
// this file is the only place in the package where that matters. The assertions
// below are absolute — exactly one allocation per emit, no more — while the
// fan-out guard compares two totals measured the same way. A collection landing
// inside a window breaks the absolute form and not the differential one: it
// drains the sync.Pool the Builder comes from, so the next few Build calls miss
// and allocate. Measured, with the whole package running so the heap is near
// its trigger: 2 007 allocations over 2 000 emits on the one window in ten that
// a collection ran through, and 2 000 exactly on the other nine — and the fail
// floated between the in-span and the out-of-span arm from run to run, which is
// what a flaky gate looks like before anyone has diagnosed it.
//
// Holding the collector off is NOT the runtime.GC() the sibling helpers refuse,
// and the difference is where the work lands. runtime.GC() returns before its
// sweep has finished, so the residual work allocates INSIDE the window — a
// first draft of the sibling helper did exactly that and reported one stray
// allocation in 10 of 12 runs. debug.SetGCPercent starts no collection: it
// waits out any mark already in flight and then stops new ones, and it is taken
// on entry, before the warm-up and long before the counter is read. The window
// allocates about 100 KB, so nothing here depends on collecting it, and the
// previous rate is restored on the way out.
func traceMallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: the argument is evaluated now and the result restored on return, so the
	//: window runs with collection off and the process leaves as it arrived.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	//: warm the recycler so the measured runs hit the pooled builder.
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

// TestT34TraceCorrelationAddsNoAllocation is the hard constraint of the trace
// correlation work: the logger's steady-state cost is EXACTLY one heap
// allocation per emit — the attrs clone the handler makes on every record —
// and stamping a record with its span must not buy a second one.
//
// It is deliberately stricter than TestV116BuildSendAllocatesOnePerEmit, which
// asserts only ">= 1" (it exists to refute a "zero-allocation" doc claim, so it
// is one-sided and would not have noticed a regression upward). This one pins
// the exact number on all three emission paths, and pins it for the in-span and
// out-of-span cases separately so the two have to agree.
//
// The correlation is allocation-free by construction: TraceContextFromContext
// returns a value type read out of the context, and the identifiers reach the
// output as 32 + 16 hex digits appended straight into the buffer the handler
// borrowed from the pool — no Go string for either id is ever materialised.
//
// It counts a TOTAL rather than calling testing.AllocsPerRun, and both of its
// assertions needed it. `plain != 1` was the weaker of the two in a way that is
// easy to miss: AllocsPerRun's `float64(mallocs / uint64(runs))` is an INTEGER
// division, so anything from exactly one allocation per emit up to just under
// two reports 1.0 — the assertion had a whole allocation of slack in it, and
// the amortised half of that slack is where a correlation defect would sit.
// `traced != plain` was worse still: both sides round the same way, so an extra
// allocation charged only to the in-span path stayed invisible until it reached
// one per record. Against traceRuns the first assertion means what it says —
// exactly one per emit, no more and no fewer. What that exactness costs is one
// extra precaution, and it is stated where it is taken: see traceMallocsOver.
//
// MUTATION-CHECKED. Giving pkg/v1/logger a package-level
// `seenTraces [][corelogger.TraceIDLen]byte` and appending the extracted
// TraceID to it inside TraceContextFromContext — the shape of any "which traces
// has this process logged for?" instrument — fails all three arms:
//
//	variadic Info, no attrs: 2000 emits inside a span performed 2013
//	  allocations, want the same 2000 as outside one
//	LogAttrs slice overload: … performed 2002 allocations …
//	Build().Send() chainable path: … performed 2001 allocations …
//
// Thirteen, then two, then one — not two thousand, and shrinking, because the
// slice is package-level and doubles: by the third arm it holds six thousand
// entries and grows once more in the whole window. That decay is the defect's
// real signature and the exact thing an average cannot represent.
// testing.AllocsPerRun measured the same mutated logger, three separate runs,
// and reported plain=1 traced=1 for every arm: `plain != 1` false,
// `traced != plain` false. The two agreed, the guard passed, and the in-span
// path really was allocating.
//
// Carries //go:build !race (the race detector allocates shadow state on every
// memory access, so any malloc count under -race measures the detector) and no
// t.Parallel (the malloc counter is process-global, so a concurrent sibling
// would corrupt the measurement). It therefore runs in exactly one lane, the
// race-off allocation lane, whose target list (tools/alloc-lane-targets.txt)
// already covers //pkg/v1/logger:logger_test — root CLAUDE.md rule 12.
func TestT34TraceCorrelationAddsNoAllocation(t *testing.T) {
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:    discardSink{},
		Encoder: logger.TextEncoder(),
	})
	if err != nil {
		t.Fatalf("NewWithSink err = %v", err)
	}
	type tc struct {
		name string
		emit func(ctx context.Context)
	}
	tests := []tc{
		{
			name: "variadic Info, no attrs",
			emit: func(ctx context.Context) { logger.Info(ctx, lg, "msg") },
		},
		{
			name: "LogAttrs slice overload",
			emit: func(ctx context.Context) { logger.LogAttrs(ctx, lg, logger.LevelInfo, "msg", allocProbeAttrs) },
		},
		{
			name: "Build().Send() chainable path",
			emit: func(ctx context.Context) {
				b := logger.Build(lg, logger.LevelInfo).Str("k", "v")
				allocSink = b
				b.Send(ctx, "msg")
			},
		},
	}
	//: measure one path under one context; the helper warms the recycler.
	measure := func(emit func(context.Context), ctx context.Context) uint64 {
		return traceMallocsOver(traceRuns, func() { emit(ctx) })
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		plain := measure(tc.emit, t.Context())
		traced := measure(tc.emit, tracedContext(t.Context()))
		//: exactly one per emit — the handler's attrs clone, and nothing else.
		//: An EXACT total, not a budget: one-per-emit times traceRuns.
		if plain != uint64(traceRuns) {
			t.Errorf("%s: %d emits with no span performed %d allocations, want exactly %d (one attrs clone each)", tc.name, traceRuns, plain, traceRuns)
		}
		//: and stamping the span buys nothing on top of it.
		if traced != plain {
			t.Errorf("%s: %d emits inside a span performed %d allocations, want the same %d as outside one", tc.name, traceRuns, traced, plain)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// TestT34TraceContextExtractionIsAllocationFree isolates the claim the emit
// path rests on: reading the span off a context allocates nothing, whether a
// span is in scope or not. If this ever regresses the per-emit budget goes with
// it, and the failure is easier to read here than through a whole Logger.
//
// MUTATION-CHECKED with the same appending seenTraces, and this is the arm that
// makes the case for counting totals on its own: running last, against a slice
// the tests above have already grown past eight thousand entries, the whole
// defect is ONE allocation in two thousand calls — and it still fails, at
// `hit — a span is in scope: 2000 calls performed 1 allocations, want 0`.
// testing.AllocsPerRun reported 0.00 for the same measurement, which is the
// integer division doing exactly what it is documented to do. The miss arm
// keeps passing under both forms, correctly — a context with no span returns
// before the append — and that asymmetry is the whole reason both cases are in
// the table.
func TestT34TraceContextExtractionIsAllocationFree(t *testing.T) {
	type tc struct {
		name string
		ctx  context.Context
	}
	tests := []tc{
		{name: "hit — a span is in scope", ctx: tracedContext(t.Context())},
		{name: "miss — no span in scope", ctx: t.Context()},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := traceMallocsOver(traceRuns, func() {
			traceAllocSink = logger.TraceContextFromContext(tc.ctx)
		})
		if got != 0 {
			t.Errorf("%s: %d calls performed %d allocations, want 0", tc.name, traceRuns, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
