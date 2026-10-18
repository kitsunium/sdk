//go:build !race

// allocwindow_test.go — the allocation window every hand-written allocation
// gate of the SDK counts with: the allocations a measured closure makes on its
// own call stack, never the process's. One instrument, copied byte for byte
// below the package clause into each package that gates on it, because a test
// helper shared across test packages would have to be an importable package,
// and an importable package is designed surface (ADR 0163). The copies are held
// equal by scripts/pre-commit/check-alloc-lane-coverage.sh, which also refuses
// a hand-written test that reads runtime.MemStats' Mallocs itself.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. Every package carrying this file is therefore listed
// in tools/alloc-lane-targets.txt, the race-off lane being its only gate (SDK
// rule 12).

package failover_test

import (
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// windowCount is what one measured window saw: the allocations a gate polices,
// and the process-wide figure it does not assert on.
type windowCount struct {
	// own is the number of allocations made on the window's own call stack —
	// every allocation the measured closure performs, however deep, and every
	// one whose recorded stack is too deep to tell (counted, never excused).
	own uint64
	// process is runtime.MemStats' Mallocs delta over the same window: every
	// goroutine and the runtime itself, which is why it is reported and not
	// gated.
	process uint64
	// foreign names the stacks process counted and own did not, for the log.
	foreign []string
}

// mallocsOver reports the TOTAL number of heap allocations f performs across
// runs calls — counted on f's own call stack, not across the whole process.
//
// testing.AllocsPerRun is deliberately not used: its last line divides as
// INTEGERS, so any defect allocating less than once per call — a slice growing
// by doubling, a cache filled on a miss — reports exactly 0.0. A mutation that
// appended to a slice on every call was measured passing against it, in
// metrics, levelgate and trace alike. A total is not subject to that rounding.
//
// A total read from runtime.MemStats alone is not the total of f either, and
// that is the flake this instrument was written for: Mallocs is process-wide,
// so it also counts whatever another goroutine or the runtime allocates while
// the window is open. GOMAXPROCS(1) does not stop that: the measured goroutine
// has usually run for more than 10 ms since its warm-up began, so sysmon may
// preempt it or take its P back during a system call (runtime.retake) and run
// something else, and a timer's goroutine runs at the first park. The console
// writer's gate failed once in the alloc lane on a loaded runner with "500
// writes through the console writer performed 6 allocations, want 0" — over a
// path with no allocation site — and passed on the rerun, unchanged; a
// goroutine made to allocate beside the window reproduces its shape at will
// (1032 process-wide, 0 on the window's stack). No tolerance answers that,
// because a tolerance also passes a real allocation. Attribution does: the
// heap profile is switched to record EVERY allocation (MemProfileRate = 1) for
// the window, and an allocation counts when its stack passes through
// allocWindow, the one frame only the measured calls have. A real allocation
// anywhere under f has that frame; a foreign one cannot. What f hands to
// another goroutine is that goroutine's, and is not counted: a gate whose
// claim covers such work measures it on the goroutine that does it.
//
// Two holes are closed rather than assumed away. The tiny allocator does not
// profile an object that fits into the current 16-byte block, but
// ReadMemStats flushes every mcache — clearing that block — right before the
// window, so the first tiny allocation f makes opens a new block and is
// profiled on f's stack. And a stack deeper than MemProfileRecord's 32 frames
// loses its root frames, allocWindow among them, so a truncated stack is
// counted as f's. TestAllocWindowCountsARealAllocation is the negative control
// that proves, in every package carrying this file, that the instrument
// records at all.
//
// The collector is held off for the window, as every former copy of this
// helper did: a collection drains pools and forces the next calls to miss,
// which would be a real allocation of f's caused by the measurement. The one
// forced cycle before the warm-up only publishes the profile; the pools it
// ages are refilled by the warm-up call, not by the window.
func mallocsOver(runs int, f func()) windowCount {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: every allocation is recorded while the window is open; the runtime
	//: re-reads the rate on the next allocation of every mcache.
	rate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = rate }()
	//: the previous percentage is restored on return — deferred, so a
	//: t.Fatalf inside f (runtime.Goexit) restores it too.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	//: one forced cycle publishes the heap profile up to here, so the diff
	//: below holds only what happened from now on.
	runtime.GC()
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	before := allocWindowProfile()
	var msBefore, msAfter runtime.MemStats
	//: ReadMemStats also flushes every mcache, which clears the tiny block.
	runtime.ReadMemStats(&msBefore)
	allocWindow(runs, f)
	runtime.ReadMemStats(&msAfter)
	//: the heap profile is published by a completed cycle; one is forced
	//: OUTSIDE the window, where what it allocates is not on allocWindow's
	//: stack (runtime.GC runs a cycle even while the collector is off).
	runtime.GC()
	after := allocWindowProfile()
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
		if allocWindowOnStack(stk) {
			got.own += uint64(d)
			continue
		}
		//: this helper's own bookkeeping, around the window, is not foreign.
		if allocWindowHasFrame(stk, selfName) {
			continue
		}
		got.foreign = append(got.foreign, fmt.Sprintf("%d by %s", d, allocWindowSummary(stk)))
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

// allocWindowProfile is the heap profile's cumulative allocation count per
// stack, freed objects included.
func allocWindowProfile() map[[32]uintptr]int64 {
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

// allocWindowOnStack reports whether an allocation with this stack was made by
// the measured calls — or may have been, because the stack was cut short.
func allocWindowOnStack(stk [32]uintptr) bool {
	if stk[len(stk)-1] != 0 {
		//: a full record is a truncated one: its root frames are gone, so it
		//: is counted against f rather than excused.
		return true
	}
	return allocWindowHasFrame(stk, allocWindowName)
}

// allocWindowHasFrame reports whether a profile stack passes through the named
// function, inlined frames included.
func allocWindowHasFrame(stk [32]uintptr, name string) bool {
	frames := runtime.CallersFrames(allocWindowTrim(stk))
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

// allocWindowSummary names the first frames of a stack, for a log line.
func allocWindowSummary(stk [32]uintptr) string {
	frames := runtime.CallersFrames(allocWindowTrim(stk))
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

// allocWindowTrim is the recorded part of a profile stack.
func allocWindowTrim(stk [32]uintptr) []uintptr {
	s := stk[:]
	for i, pc := range s {
		if pc == 0 {
			return s[:i]
		}
	}
	return s
}

// reportForeign logs what the process allocated beside the window, which is
// not the gate's to answer for and is exactly what used to fail it.
func reportForeign(t *testing.T, what string, got windowCount) {
	t.Helper()
	if len(got.foreign) > 0 {
		t.Logf("%s: %d allocations process-wide, %d on the window's stack; not counted: %s",
			what, got.process, got.own, strings.Join(got.foreign, "; "))
	}
}

// allocWindowControlRuns is how many calls the negative control measures.
const allocWindowControlRuns int = 500

// allocWindowControlSink keeps the control's allocation reachable, so escape
// analysis cannot place it on the stack.
var allocWindowControlSink []byte

// allocWindowControlTiny is the same for the control's tiny allocation.
var allocWindowControlTiny *uint32

// TestAllocWindowCountsARealAllocation is the negative control of mallocsOver:
// an instrument that attributes by stack and reads a profile only a collection
// publishes would pass every gate of its package if either half stopped
// working, so it is made to see allocations it knows are there — one per call,
// one on a single call of the window, and a tiny one, which the tiny allocator
// would hide if the block were not cleared.
func TestAllocWindowCountsARealAllocation(t *testing.T) {
	got := mallocsOver(allocWindowControlRuns, func() {
		allocWindowControlSink = make([]byte, 64)
	})
	if got.own != uint64(allocWindowControlRuns) {
		t.Fatalf("the window counted %d of the %d allocations it was shown (process-wide %d)",
			got.own, allocWindowControlRuns, got.process)
	}
	calls := 0
	got = mallocsOver(allocWindowControlRuns, func() {
		calls++
		if calls == allocWindowControlRuns/2 {
			allocWindowControlSink = make([]byte, 64)
		}
	})
	if got.own != 1 {
		t.Fatalf("the window counted %d allocations where one call of %d made one (process-wide %d)",
			got.own, allocWindowControlRuns, got.process)
	}
	got = mallocsOver(allocWindowControlRuns, func() {
		allocWindowControlTiny = new(uint32)
	})
	if got.own == 0 {
		t.Fatalf("the window counted none of %d tiny allocations (process-wide %d)",
			allocWindowControlRuns, got.process)
	}
}
