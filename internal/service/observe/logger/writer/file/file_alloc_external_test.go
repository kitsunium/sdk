//go:build !race

// Package file_test — what this writer adds to the SDK's flagship allocation
// claim, which is nothing, gated.
//
// The root CLAUDE.md sells the logger on "one alloc per emit" and pins it in
// pkg/v1/observe/logger with TestPerfAllocsBuildSend. That test ends at
// the handler. Nobody had ever asked what a WRITER adds underneath it, and this
// is the file half of the answer: the allocation profile of a full emit
// attributes 95.92 % of its objects to the handler's attrs clone and 0 to the
// transport (BENCH.md §5). This file makes that a property rather than an
// observation.
//
// Flush is measured beside Write because it is the one call on this Sink that
// reaches a device, and "fsync costs 78 773 ns on ext4" would be a very easy
// place to hide an allocation nobody would notice against that denominator.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite,
// which is why //internal/service/observe/logger/writer/file:file_test carries an entry in
// tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package file_test

import (
	"context"
	"path/filepath"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	servicelogger "github.com/kitsunium/sdk/internal/service/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// allocRuns is how many times each claim is exercised. It is large enough that
// an amortised allocation — one that happens on a growth step rather than on
// every call — has happened several times by the end.
const allocRuns int = 500

// allocRuntimeWarmup is how many Flush calls, and how many emits through each
// of two throwaway handlers, warmRuntimeCaches spends before any window is
// measured. It is not about this package.
//
// Flush reaches the file through a type switch over non-empty interfaces, and
// the runtime serves those from a per-call-site cache it builds LAZILY on
// purpose: runtime/iface.go gates the build behind `cheaprand()&1023 != 0`, so
// about one miss in 1024 pays for it and buildInterfaceSwitchCache allocates.
// The result is a handful of allocations landing at an unpredictable point
// roughly a thousand calls into the process, attributable to no line here.
//
// It is invisible to testing.AllocsPerRun — 6 over 500 is 0 after the integer
// division — and it is precisely what a total-counting window sees. Measured:
// this guard failed 2 runs in 5 under Bazel's sandbox at "500 flushes performed
// 6 allocations", while passing 12 of 12 locally on both tmpfs and ext4, and
// holding the collector off did NOT fix it. Thirty thousand calls put the
// probability the cache is still unbuilt at (1023/1024)^30000, about 2e-13.
//
// The emit path has its own call sites, and warming Flush alone left them
// cold: the cache is built on the emitting goroutine, so it lands on the
// window's own stack, and on windows/amd64 the transport-free arm caught it —
// "500 emits through the file writer performed 500 allocations against 501
// with no transport". The emit is warmed too, through a throwaway handler over
// each transport the two arms use, as the console writer's guard does.
//
// The THROWAWAY sink is load-bearing rather than tidy. The cache is the
// runtime's — per call site, process-global — so any sink can pay for it, while
// what this file polices is per sink. Warming through the sink under test would
// leave an accumulating regression far past its own growth steps, which is the
// same blindness the integer division produces and the reason this file counts
// totals at all.
const allocRuntimeWarmup int = 30000

// allocNoopSink is the control transport: it accepts the bytes and does
// nothing, so an emit through it costs the handler and the encoder alone.
type allocNoopSink struct{}

func (allocNoopSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: accept the whole payload with no work.
	return len(p), nil
}
func (allocNoopSink) Flush(_ context.Context) error { return nil }
func (allocNoopSink) Close() error                  { return nil }

// allocSink is the sink writer.Open("file", cfg) hands back over a fresh file
// in the test's own directory — the shipped composition, not a reassembly of
// it, and carrying a genuine write(2).
// warmRuntimeCaches builds the runtime's lazy interface-switch caches on a sink
// nothing else will touch, so no measured window pays for them.
func warmRuntimeCaches(t *testing.T) {
	t.Helper()
	throwaway := allocSink(t, "runtime-warmup.log")
	ctx := context.Background()
	for range allocRuntimeWarmup {
		if err := throwaway.Flush(ctx); err != nil {
			t.Fatalf("warming the runtime caches: %v", err)
		}
	}
	//: the emit path both arms walk, over each of their transports, through
	//: handlers no window measures.
	rec := corelogger.RecordEvent{Level: level.Info, Message: "warm"}
	for _, sink := range []corelogger.Sink{allocNoopSink{}, allocSink(t, "runtime-warmup-emit.log")} {
		h, err := servicelogger.NewHandler(encoder.NewText(clock.System), sink, level.Info)
		if err != nil {
			t.Fatalf("building the warm-up handler: %v", err)
		}
		for range allocRuntimeWarmup {
			if herr := h.Handle(ctx, rec); herr != nil {
				t.Fatalf("warming the runtime caches: %v", herr)
			}
		}
	}
}

func allocSink(t *testing.T, name string) corelogger.Sink {
	t.Helper()
	s, err := writer.Open("file", writer.FileConfig{Path: filepath.Join(t.TempDir(), name)})
	if err != nil {
		t.Fatalf("writer.Open(file): %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Logf("closing the sink: %v", cerr)
		}
	})
	//: the real product of the real factory.
	return s
}

// TestFileWriterAddsNoAllocationToAnEmit is the guard behind this package's
// share of the "one alloc per emit" claim.
//
// Three assertions. Write and Flush on their own allocating nothing is
// necessary but says nothing about composition; what a consumer is sold is that
// installing the default file writer does not change the per-emit allocation
// count at all. So the emit is measured twice — once with no transport under
// the handler and once with this writer — and the two totals must be EQUAL.
// Asserting a constant instead would couple this package to the handler's
// attrs clone, which is not its property and is already pinned in pkg/v1/observe/logger.
//
// MUTATION: giving Open a per-record decorator — the change a contributor makes
// the day a per-writer feature has to touch the bytes, here
// `return levelgate.New(&copySink{inner: base}, c.MinLevel)` with a copySink
// whose Write does `s.inner.Write(ctx, r, append([]byte(nil), p...))` — fails
// both at `500 writes through the file writer performed 500 allocations on
// their own stack (500 process-wide), want 0`
// and at `500 emits through the file writer performed 1000 allocations against
// 500 with no transport; the writer must add none`. The second line is the
// interesting one: it is the same defect stated as what a consumer would
// actually notice, a doubling of the logger's advertised allocation budget.
func TestFileWriterAddsNoAllocationToAnEmit(t *testing.T) {
	ctx := context.Background()
	//: before anything is measured, and never on the sink under test.
	warmRuntimeCaches(t)
	sink := allocSink(t, "alloc.log")
	rec := corelogger.RecordEvent{Level: level.Info}
	line := []byte("2026-09-10T20:15:11.482Z INFO msg=\"request served\" status=200\n")

	writes := mallocsOver(allocRuns, func() {
		if _, err := sink.Write(ctx, rec, line); err != nil {
			t.Fatalf("write: %v", err)
		}
	})
	reportWindow(t, "writes", writes)
	if writes.own != 0 {
		t.Errorf("%d writes through the file writer performed %d allocations on their own stack (%d process-wide), want 0",
			allocRuns, writes.own, writes.process)
	}

	//: fsync is expensive enough to hide an allocation behind; it must not.
	flushes := mallocsOver(allocRuns, func() {
		if err := sink.Flush(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
	})
	reportWindow(t, "flushes", flushes)
	if flushes.own != 0 {
		t.Errorf("%d flushes through the file writer performed %d allocations on their own stack (%d process-wide), want 0",
			allocRuns, flushes.own, flushes.process)
	}

	//: the same emit twice: once with no transport, once with this writer.
	bare := emitCost(t, allocNoopSink{})
	through := emitCost(t, allocSink(t, "emit.log"))
	reportWindow(t, "emits with no transport", bare)
	reportWindow(t, "emits through the file writer", through)
	if through.own != bare.own {
		t.Errorf("%d emits through the file writer performed %d allocations against %d with no transport; the writer must add none",
			allocRuns, through.own, bare.own)
	}
}

// emitCost totals the allocations of allocRuns full emits through sink: the
// text encoder and the generic handler above it, which is the shape a consumer
// runs.
func emitCost(t *testing.T, sink corelogger.Sink) windowCount {
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
