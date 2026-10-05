//go:build !race

// The fixtures of the logger's performance contracts (design/sdk.yaml,
// budgets): each sets one call up and returns it, and the perf_gen_test.go
// kit gen writes beside this file counts its allocations against its
// budget — the total over 30 000 calls after as many warm-up calls.
//
// The two contracts were hand-written tests of this package, their bounds
// kept exactly:
//
//   - Build — TestV116BuildSendAllocatesOnePerEmit (builder_integration_test.go):
//     the chainable Build().Send() hot path is NOT allocation-free. The
//     handler clones the accumulated attrs scratchpad (mergeAttrs /
//     slices.Clone) on every Send, so at least one heap slice escapes per
//     emit even though the sync.Pool recycles the builder. Regression for
//     V116: BENCH.md once claimed the byte cost "drops to near-zero" on this
//     path. The bound is the old one, a FLOOR — allocs/op >= 1 there,
//     allocsMin: 1 here — the guard behind the "one alloc per emit" claim in
//     README.md and CLAUDE.md.
//   - TraceContextFromContext — TestT34TraceContextExtractionIsAllocationFree
//     (tracecontext_integration_test.go): reading the span off a context
//     allocates nothing, whether a span is in scope or not; the emit path's
//     per-record budget rests on it. Both cases are in the one call, and that
//     asymmetry is why: MUTATION-CHECKED with an appending seenTraces, the hit
//     arm failed at ONE allocation in two thousand calls (the slice grown
//     past eight thousand entries by the tests before it), which
//     testing.AllocsPerRun read as 0.00, while the miss arm — a context with
//     no span returns before the append — passed under both forms.
//
// Race off: testing.AllocsPerRun and every malloc count read +1 under
// -race. The race-off allocation lane runs this package.
package logger_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// perfTrace keeps the extracted trace context alive.
var perfTrace logger.TraceContext

// perfBuilder keeps the builder alive, as the old probe's allocSink did.
var perfBuilder any

// perfBuildSend is Build's fixture: one chainable emit, three attributes,
// through a Logger writing to a discarding sink with the text encoder.
func perfBuildSend(tb testing.TB) func() {
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:    discardSink{},
		Encoder: logger.TextEncoder(),
	})
	if err != nil {
		tb.Fatalf("NewWithSink err = %v", err)
	}
	ctx := tb.Context()
	return func() {
		b := logger.Build(lg, logger.LevelInfo).
			Str("k1", "v1").
			Str("k2", "v2").
			Int("n1", 1)
		perfBuilder = b
		b.Send(ctx, "msg")
	}
}

// perfTraceContext is TraceContextFromContext's fixture: a context with a
// span in scope, then one without.
func perfTraceContext(tb testing.TB) func() {
	hit, miss := tracedContext(tb.Context()), tb.Context()
	return func() {
		perfTrace = logger.TraceContextFromContext(hit)
		perfTrace = logger.TraceContextFromContext(miss)
	}
}
