//go:build !race

// Package console_test — what this writer adds to the SDK's flagship
// allocation claim, which is nothing, gated.
//
// The root CLAUDE.md sells the logger on "one alloc per emit" and pins it in
// pkg/v1/observe/logger with TestPerfAllocsBuildSend. That test ends at
// the handler. Nobody had ever asked what a WRITER adds underneath it, and this
// is the console half of the answer: the allocation profile of a full emit
// attributes 99.53 % of its objects to the handler's attrs clone and 0 to the
// transport (BENCH.md §5). This file makes that a property rather than an
// observation.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite,
// which is why //internal/service/observe/logger/writer/console:console_test carries an entry
// in tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package console_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
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

// allocRuntimeWarmup is how many emits warmRuntimeCaches spends on a THROWAWAY
// handler before any window is measured. It is not about this package.
//
// An emit reaches the sink through type switches over non-empty interfaces, and
// the runtime serves those from a per-call-site cache it builds LAZILY on
// purpose: runtime/iface.go gates the build behind `cheaprand()&1023 != 0`, so
// about one miss in 1024 pays for it and buildInterfaceSwitchCache allocates.
// The result is a handful of allocations landing at an unpredictable point
// roughly a thousand calls into the process, attributable to no line here.
//
// It is invisible to testing.AllocsPerRun — 6 over 500 is 0 after the integer
// division — and it is exactly what a total-counting window sees. Measured: this
// guard failed 1 run in 10 under Bazel at "500 emits performed 506 allocations
// against 500 with no transport", the sibling file guard failed 2 in 5 the same
// way, and holding the collector off fixed NEITHER. Thirty thousand emits put
// the probability the cache is still unbuilt at (1023/1024)^30000, about 2e-13.
//
// The THROWAWAY handler is load-bearing rather than tidy. The cache is the
// runtime's — per call site, process-global — so any handler can pay for it,
// while what this file polices is per writer. Warming through the object under
// test would leave an accumulating regression far past its own growth steps,
// which is the same blindness the integer division produces.
const allocRuntimeWarmup int = 30000

// windowCount is what one measured window saw: the allocations this file
// polices, and the process-wide figure it no longer asserts on.
type windowCount struct {
	// own is the number of allocations made on the window's own call stack — every
	// allocation the measured closure performs, however deep, and every one whose
	// recorded stack is too deep to tell (counted, never excused).
	own uint64
	// process is runtime.MemStats' Mallocs delta over the same window: every
	// goroutine and the runtime itself, which is why it is reported and not gated.
	process uint64
	// foreign names the stacks process counted and own did not, for the log.
	foreign []string
}

// mallocsOver reports the TOTAL number of heap allocations f performs across
// runs calls — counted on f's own call stack, not across the whole process.
//
// testing.AllocsPerRun is deliberately not used, and the reason is recorded in
// full in internal/service/observe/logger/writer/levelgate's copy of this helper: its last
// line divides as INTEGERS, so any defect allocating less than once per call
// reports exactly 0.0. A mutation that appended to a slice on every call was
// measured passing against it. A total is not subject to that rounding.
//
// A total read from runtime.MemStats alone is not the total of f either, and
// that is the flake this helper was rewritten for: Mallocs is process-wide, so
// it also counts whatever another goroutine or the runtime allocates while the
// window is open — GOMAXPROCS(1) does not stop that: the measured goroutine has
// usually run for more than 10 ms since the warm-up began, so sysmon may preempt
// it or take its P back during a write(2) (runtime.retake) and run something
// else. This gate failed once in the alloc lane, on a loaded runner, with
// "500 writes through the console writer performed 6 allocations, want 0" — over
// a path with no allocation site — and passed on the rerun, unchanged. It never
// reproduced locally in 1 300 fresh processes, CPU contention and GOGC=1
// included; a goroutine made to allocate beside the window reproduces its shape
// at will (1032 process-wide, 0 on the window's stack). No tolerance answers
// that, because a tolerance also passes a real allocation. Attribution does: the
// heap profile is switched to record EVERY allocation (MemProfileRate = 1) for
// the window, and an allocation counts when its stack passes through
// allocWindow, the one frame only the measured calls have. A real allocation
// anywhere under f has that frame; a foreign one cannot.
//
// Two holes are closed rather than assumed away. The tiny allocator does not
// profile an object that fits into the current 16-byte block, but
// ReadMemStats flushes every mcache — clearing that block — right before the
// window, so the first tiny allocation f makes opens a new block and is
// profiled on f's stack. And a stack deeper than MemProfileRecord's 32 frames
// loses its root frames, allocWindow among them, so a truncated stack is
// counted as f's. TestAllocWindowCountsARealAllocation is the negative control
// that proves the instrument records at all.
func mallocsOver(runs int, f func()) windowCount {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: every allocation is recorded while the window is open; the runtime
	//: re-reads the rate on the next allocation of every mcache.
	rate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = rate }()
	//: collection is held off for the window: a collection drains pools and
	//: forces the next calls to miss, which would be a real allocation of f's
	//: caused by the measurement. The previous rate is restored after it.
	gcPercent := debug.SetGCPercent(-1)
	//: one forced cycle publishes the heap profile up to here, so the diff below
	//: holds only what happened from now on; it runs BEFORE the warm-up call so
	//: the pools it ages are refilled by that call, not by the window.
	runtime.GC()
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	before := profileByStack()
	var msBefore, msAfter runtime.MemStats
	//: ReadMemStats also flushes every mcache, which clears the tiny block.
	runtime.ReadMemStats(&msBefore)
	allocWindow(runs, f)
	runtime.ReadMemStats(&msAfter)
	debug.SetGCPercent(gcPercent)
	//: the heap profile is published by a completed cycle; one is forced
	//: OUTSIDE the window, where what it allocates is not on allocWindow's stack.
	runtime.GC()
	after := profileByStack()
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	got := windowCount{process: msAfter.Mallocs - msBefore.Mallocs}
	//: this helper's own frame: what it allocates around the window is
	//: neither the measured calls' nor foreign.
	self, _, _, _ := runtime.Caller(0)
	selfName := runtime.FuncForPC(self).Name()
	for stk, n := range after {
		d := n - before[stk]
		if d <= 0 {
			continue
		}
		if onWindowStack(stk) {
			got.own += uint64(d)
			continue
		}
		//: this helper's own bookkeeping, around the window, is not foreign.
		if hasFrame(stk, selfName) {
			continue
		}
		got.foreign = append(got.foreign, fmt.Sprintf("%d by %s", d, stackSummary(stk)))
	}
	return got
}

// allocWindow runs f runs times. It is a frame of its own, never inlined, so
// that every allocation the measured calls make carries it on its stack.
//
//go:noinline
func allocWindow(runs int, f func()) {
	for range runs {
		f()
	}
}

// allocWindowName is allocWindow's symbol, as the runtime spells it in a stack.
var allocWindowName = runtime.FuncForPC(reflect.ValueOf(allocWindow).Pointer()).Name()

// profileByStack is the heap profile's cumulative allocation count per stack,
// freed objects included.
func profileByStack() map[[32]uintptr]int64 {
	n, _ := runtime.MemProfile(nil, true)
	var recs []runtime.MemProfileRecord
	//: the profile can grow between the sizing call and the read; retry larger.
	for {
		recs = make([]runtime.MemProfileRecord, n+64)
		var ok bool
		n, ok = runtime.MemProfile(recs, true)
		if ok {
			recs = recs[:n]
			break
		}
	}
	out := make(map[[32]uintptr]int64, len(recs))
	for i := range recs {
		out[recs[i].Stack0] += recs[i].AllocObjects
	}
	return out
}

// onWindowStack reports whether an allocation with this stack was made by the
// measured calls — or may have been, because the stack was cut short.
func onWindowStack(stk [32]uintptr) bool {
	if stk[len(stk)-1] != 0 {
		//: a full record is a truncated one: its root frames are gone, so it is
		//: counted against f rather than excused.
		return true
	}
	return hasFrame(stk, allocWindowName)
}

// hasFrame reports whether a profile stack passes through the named function,
// inlined frames included.
func hasFrame(stk [32]uintptr, name string) bool {
	frames := runtime.CallersFrames(trimStack(stk))
	for {
		fr, more := frames.Next()
		if fr.Function == name {
			return true
		}
		if !more {
			return false
		}
	}
}

// stackSummary names the first frames of a stack, for a log line.
func stackSummary(stk [32]uintptr) string {
	frames := runtime.CallersFrames(trimStack(stk))
	var names []string
	for len(names) < 4 {
		fr, more := frames.Next()
		names = append(names, fr.Function)
		if !more {
			break
		}
	}
	return strings.Join(names, " < ")
}

// trimStack is the recorded part of a profile stack.
func trimStack(stk [32]uintptr) []uintptr {
	s := stk[:]
	for i, pc := range s {
		if pc == 0 {
			return s[:i]
		}
	}
	return s
}

// allocControlSink keeps the control's allocation reachable, so escape
// analysis cannot place it on the stack.
var allocControlSink []byte

// allocControlTiny is the same for the control's tiny allocation.
var allocControlTiny *uint32

// TestAllocWindowCountsARealAllocation is the negative control of mallocsOver:
// an instrument that attributes by stack and reads a profile only a collection
// publishes would pass every gate in this file if either half stopped working,
// so it is made to see an allocation it knows is there — one per call, plus a
// tiny one, which the tiny allocator would hide if the block were not cleared.
func TestAllocWindowCountsARealAllocation(t *testing.T) {
	got := mallocsOver(allocRuns, func() {
		allocControlSink = make([]byte, 64)
	})
	if got.own != uint64(allocRuns) {
		t.Fatalf("the window counted %d of the %d allocations it was shown (process-wide %d)", got.own, allocRuns, got.process)
	}
	got = mallocsOver(allocRuns, func() {
		allocControlTiny = new(uint32)
	})
	if got.own == 0 {
		t.Fatalf("the window counted none of %d tiny allocations (process-wide %d)", allocRuns, got.process)
	}
}

// reportForeign logs what the process allocated beside the window, which is
// not this package's to answer for and is exactly what used to fail the gate.
func reportForeign(t *testing.T, what string, got windowCount) {
	t.Helper()
	if len(got.foreign) > 0 {
		t.Logf("%s: %d allocations process-wide, %d on the window's stack; not counted: %s",
			what, got.process, got.own, strings.Join(got.foreign, "; "))
	}
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
// pkg/v1/observe/logger.
//
// MUTATION: giving Open a per-record decorator — the change a contributor makes
// the day a per-writer feature has to touch the bytes, here
// `return levelgate.New(&copySink{inner: pickStream(c.Stream)}, c.MinLevel)`
// with a copySink whose Write does `s.inner.Write(ctx, r, append([]byte(nil),
// p...))` — fails both at
// `500 writes through the console writer performed 500 allocations on their own
// stack (500 process-wide), want 0` and at `500 emits through the console writer
// performed 1000 allocations against 500 with no transport; the writer must add
// none`. The second line is the interesting one: it is the same defect stated as
// what a consumer would actually notice, a doubling of the logger's advertised
// allocation budget.
//
// The stack attribution of mallocsOver was mutation-checked against the same
// write path (sink/console's Write) with four more defects, each failing the
// first assertion: a copy of p per write (500), an append to a package slice —
// an amortised growth, 6 over the window —, a tiny 4-byte allocation per write
// (125: one per 16-byte block the tiny allocator opens), and ONE allocation on
// the 300th write only, large or tiny (1). A goroutine allocating beside the
// window, the flake's shape, was measured at 1032 process-wide and 0 counted.
func TestConsoleWriterAddsNoAllocationToAnEmit(t *testing.T) {
	ctx := context.Background()
	//: before anything is measured, and never through the sink under test.
	warmRuntimeCaches(t)
	sink := allocSink(t)
	rec := corelogger.RecordEvent{Level: level.Info}
	line := []byte("2026-09-10T20:15:11.482Z INFO msg=\"request served\" status=200\n")

	writes := mallocsOver(allocRuns, func() {
		if _, err := sink.Write(ctx, rec, line); err != nil {
			t.Fatalf("write: %v", err)
		}
	})
	reportForeign(t, "writes", writes)
	if writes.own != 0 {
		t.Errorf("%d writes through the console writer performed %d allocations on their own stack (%d process-wide), want 0",
			allocRuns, writes.own, writes.process)
	}

	//: the same emit twice: once with no transport, once with this writer.
	bare := emitCost(t, allocNoopSink{})
	through := emitCost(t, sink)
	reportForeign(t, "emits with no transport", bare)
	reportForeign(t, "emits through the console writer", through)
	if through.own != bare.own {
		t.Errorf("%d emits through the console writer performed %d allocations against %d with no transport; the writer must add none",
			allocRuns, through.own, bare.own)
	}
}

// warmRuntimeCaches builds the runtime's lazy interface-switch caches through a
// handler and sink nothing else will touch, so no measured window pays for them.
func warmRuntimeCaches(t *testing.T) {
	t.Helper()
	h, err := servicelogger.NewHandler(encoder.NewText(clock.System), allocNoopSink{}, level.Info)
	if err != nil {
		t.Fatalf("building the warm-up handler: %v", err)
	}
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info, Message: "warm"}
	//: the emit path is what the measured windows walk, so it is what is warmed.
	for range allocRuntimeWarmup {
		if herr := h.Handle(ctx, rec); herr != nil {
			t.Fatalf("warming the runtime caches: %v", herr)
		}
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
