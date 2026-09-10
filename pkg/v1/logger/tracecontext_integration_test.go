//go:build !race

package logger_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// traceAllocSink defeats dead-code elimination in the extraction probe. It is
// TYPED rather than an any: assigning a 24-byte struct into an interface boxes
// it, which is one heap allocation of the test's own making and would hide the
// very number this file measures.
var traceAllocSink logger.TraceContext

// allocProbeAttrs is pre-built outside the measured closure so the variadic
// slice is not re-allocated per call — the emit paths below then differ only
// in whether a span is in scope.
var allocProbeAttrs = []logger.Attr{logger.String("k", "v")}

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
// Carries //go:build !race (testing.AllocsPerRun reports +1 under -race) and
// no t.Parallel (AllocsPerRun reads a process-global counter). It therefore
// runs in exactly one lane, the race-off allocation lane, whose target list
// (tools/alloc-lane-targets.txt) already covers //pkg/v1/logger:logger_test —
// root CLAUDE.md rule 12.
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
	//: measure one path under one context, after warming the recycler.
	measure := func(emit func(context.Context), ctx context.Context) float64 {
		emit(ctx)
		return testing.AllocsPerRun(2000, func() { emit(ctx) })
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		plain := measure(tc.emit, t.Context())
		traced := measure(tc.emit, tracedContext(t.Context()))
		//: exactly one — the handler's attrs clone, and nothing else.
		if plain != 1 {
			t.Errorf("%s: allocs/op with no span = %v, want exactly 1 (the handler's attrs clone)", tc.name, plain)
		}
		//: and stamping the span buys nothing on top of it.
		if traced != plain {
			t.Errorf("%s: allocs/op inside a span = %v, want the same %v as outside one", tc.name, traced, plain)
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
		got := testing.AllocsPerRun(2000, func() {
			traceAllocSink = logger.TraceContextFromContext(tc.ctx)
		})
		if got != 0 {
			t.Errorf("%s: TraceContextFromContext allocs/op = %v, want 0", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
