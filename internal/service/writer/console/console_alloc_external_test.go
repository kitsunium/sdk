//go:build !race

// Package console_test — what this writer adds to the SDK's flagship
// allocation claim, which is nothing, gated.
//
// The root CLAUDE.md sells the logger on "one alloc per emit" and pins it in
// pkg/v1/logger with TestV116BuildSendAllocatesOnePerEmit. That test ends at
// the handler. Nobody had ever asked what a WRITER adds underneath it, and this
// is the console half of the answer: the allocation profile of a full emit
// attributes 99.53 % of its objects to the handler's attrs clone and 0 to the
// transport (BENCH.md §5). This file makes that a property rather than an
// observation.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite,
// which is why //internal/service/writer/console:console_test carries an entry
// in tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package console_test

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	servicelogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

// allocRuns is how many times each claim is exercised. It is large enough that
// an amortised allocation — one that happens on a growth step rather than on
// every call — has happened several times by the end.
const allocRuns int = 500

// mallocsOver reports the TOTAL number of heap allocations f performs across
// runs calls, rather than the per-call average.
//
// testing.AllocsPerRun is deliberately not used, and the reason is recorded in
// full in internal/service/writer/levelgate's copy of this helper: its last
// line divides as INTEGERS, so any defect allocating less than once per call
// reports exactly 0.0. A mutation that appended to a slice on every call was
// measured passing against it. A total is not subject to that rounding.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: collection is held off for the window — the sibling file guard failed exactly this way under Bazel while
	//: passing locally, so the parade is taken here too rather than waiting for
	//: the same afternoon to be spent twice.
	//: A collection is not free of allocations from the measured goroutine's
	//: point of view; it drains pools and forces the next calls to miss.
	//: The argument is evaluated now and the previous rate restored on return.
	//: NOT runtime.GC(), which returns before its sweep finishes and therefore
	//: allocates INSIDE the window it was meant to clear.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	var before, after runtime.MemStats
	//: no runtime.GC() here, and that is deliberate: an explicit collection
	//: returns before its sweep is finished, so the residual work allocates
	//: INSIDE the window below. A first draft of this helper called it and
	//: reported exactly 1 stray allocation in 10 of 12 runs; the allocation
	//: profile attributed it to the runtime.GC() line. testing.AllocsPerRun
	//: does not call GC either, for the same reason.
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	return after.Mallocs - before.Mallocs
}

// allocSink is the sink writer.Open("console", cfg) hands back, with os.Stderr
// pointed at /dev/null for the duration of the construction. The destination is
// a real descriptor rather than an io.Writer double on purpose: this must be
// the shipped composition carrying a genuine write(2), because a double that
// implements fewer interfaces than *os.File would be a different code path.
func allocSink(t *testing.T) corelogger.Sink {
	t.Helper()
	null, oerr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if oerr != nil {
		t.Fatalf("opening %s: %v", os.DevNull, oerr)
	}
	t.Cleanup(func() {
		if cerr := null.Close(); cerr != nil {
			t.Logf("closing %s: %v", os.DevNull, cerr)
		}
	})
	saved := os.Stderr
	os.Stderr = null
	s, err := writer.Open("console", writer.ConsoleConfig{})
	os.Stderr = saved
	if err != nil {
		t.Fatalf("writer.Open(console): %v", err)
	}
	//: the real product of the real factory, pointed somewhere reproducible.
	return s
}

// allocNoopSink is the control transport: it accepts the bytes and does
// nothing, so an emit through it costs the handler and the encoder alone.
type allocNoopSink struct{}

func (allocNoopSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: accept the whole payload with no work.
	return len(p), nil
}
func (allocNoopSink) Flush(_ context.Context) error { return nil }
func (allocNoopSink) Close() error                  { return nil }

// TestConsoleWriterAddsNoAllocationToAnEmit is the guard behind this package's
// share of the "one alloc per emit" claim.
//
// Two assertions, and the second is the one that matters. A Write on its own
// allocating nothing is necessary but says nothing about composition; what a
// consumer is sold is that installing the default writer does not change the
// per-emit allocation count at all. So the emit is measured twice — once with
// no transport under the handler and once with this writer — and the two totals
// must be EQUAL. Asserting a constant instead would couple this package to the
// handler's attrs clone, which is not its property and is already pinned in
// pkg/v1/logger.
//
// MUTATION: giving Open a per-record decorator — the change a contributor makes
// the day a per-writer feature has to touch the bytes, here
// `return levelgate.New(&copySink{inner: pickStream(c.Stream)}, c.MinLevel)`
// with a copySink whose Write does `s.inner.Write(ctx, r, append([]byte(nil),
// p...))` — fails both at
// `500 writes through the console writer performed 500 allocations, want 0` and
// at `500 emits through the console writer performed 1000 allocations against
// 500 with no transport; the writer must add none`. The second line is the
// interesting one: it is the same defect stated as what a consumer would
// actually notice, a doubling of the logger's advertised allocation budget.
func TestConsoleWriterAddsNoAllocationToAnEmit(t *testing.T) {
	ctx := context.Background()
	sink := allocSink(t)
	rec := corelogger.RecordEvent{Level: level.Info}
	line := []byte("2026-09-10T20:15:11.482Z INFO msg=\"request served\" status=200\n")

	if got := mallocsOver(allocRuns, func() {
		if _, err := sink.Write(ctx, rec, line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}); got != 0 {
		t.Errorf("%d writes through the console writer performed %d allocations, want 0", allocRuns, got)
	}

	//: the same emit twice: once with no transport, once with this writer.
	bare := emitCost(t, allocNoopSink{})
	through := emitCost(t, sink)
	if through != bare {
		t.Errorf("%d emits through the console writer performed %d allocations against %d with no transport; the writer must add none",
			allocRuns, through, bare)
	}
}

// emitCost totals the allocations of allocRuns full emits through sink: the
// text encoder and the generic handler above it, which is the shape a consumer
// runs.
func emitCost(t *testing.T, sink corelogger.Sink) uint64 {
	t.Helper()
	h, err := servicelogger.NewHandler(encoder.NewText(clock.System), sink, level.Info)
	if err != nil {
		t.Fatalf("building the handler: %v", err)
	}
	rec := corelogger.RecordEvent{
		Level:   level.Info,
		Message: "request served",
		Attrs: []corelogger.AttrValue{
			{Key: "method", Value: corelogger.StringValue("GET")},
			{Key: "status", Value: corelogger.IntValue(200)},
		},
	}
	ctx := context.Background()
	//: the total, not the average — see mallocsOver.
	return mallocsOver(allocRuns, func() {
		if herr := h.Handle(ctx, rec); herr != nil {
			t.Fatalf("handle: %v", herr)
		}
	})
}
