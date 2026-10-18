//go:build !race

package logger_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// traceRuns is how many emits each arm performs inside the measured window. A
// per-emit cost shows up as traceRuns allocations — the number the first
// assertion below is written against — and an amortised one as roughly its
// base-two logarithm, which is what the old form of these assertions could not
// see at all.
const traceRuns int = 2000

// allocProbeAttrs is pre-built outside the measured closure so the variadic
// slice is not re-allocated per call — the emit paths below then differ only
// in whether a span is in scope.
var allocProbeAttrs = []logger.Attr{logger.String("k", "v")}

// TestT34TraceCorrelationAddsNoAllocation is the hard constraint of the trace
// correlation work: the logger's steady-state cost is EXACTLY one heap
// allocation per emit — the attrs clone the handler makes on every record —
// and stamping a record with its span must not buy a second one.
//
// It is deliberately stricter than TestPerfAllocsBuildSend, which
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
// exactly one per emit, no more and no fewer. What that exactness costs is
// stated where it is paid, in allocwindow_test.go's mallocsOver: the collector
// is held off for the window, since a collection drains the sync.Pool the
// Builder comes from (measured, 2 007 allocations over 2 000 emits on the one
// window in ten a collection ran through), and only the window's own stack is
// counted, since the runtime or a timer can allocate beside it even at
// GOMAXPROCS 1 — on macOS and Windows often enough to fail an exact count,
// which this file used to answer with the smallest of five windows.
//
// MUTATION-CHECKED. Giving pkg/v1/observe/logger a package-level
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
// t.Parallel (mallocsOver sets the process's profile rate, collector and
// GOMAXPROCS for its window). It therefore runs in exactly one lane, the
// race-off allocation lane, whose target list (tools/alloc-lane-targets.txt)
// already covers //pkg/v1/observe/logger:logger_test — root CLAUDE.md rule 12.
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
		got := mallocsOver(traceRuns, func() { emit(ctx) })
		reportForeign(t, "emits", got)
		return got.own
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
