//go:build !race

// The fixtures of swar's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the gen_perf_test.go kit gen
// writes beside this file counts its allocations against its budget — the
// total over 30 000 calls after as many warm-up calls.
//
// The claim is that a search reads a slice and allocates nothing, and the
// shape that loses it is a slice header or a closure that escapes: a Set
// passed by pointer into a function value, a variadic list of bytes built per
// call. Every functional test passes against either; the price is paid per
// header line, per JSON string. Each fixture's call walks both lengths a form
// takes — a short slice, which the SWAR form serves on every build, and a long
// one, which the vector form serves where there is one — and a hit as well as
// a miss, so that every path of the dispatcher is inside the window.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so a malloc count under `-race`
// measures the detector. The allocation lane runs this package, through the
// section kit gen keeps of tools/alloc-lane-targets.txt (SDK-wide rule 12).
package swar_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/swar"
)

// The sinks keep each call's results alive.
var (
	perfInt int
	perfSet swar.Set
)

// perfInputs are the two slices every fixture searches: short, 12 bytes
// ending in the byte to find, and long, 100 bytes with it at the end. A miss
// is the same slice searched for a byte it does not hold.
func perfInputs() (short, long []byte) {
	short = append(bytes.Repeat([]byte{'a'}, 11), '\n')
	long = append(bytes.Repeat([]byte{'a'}, 99), '\n')
	return short, long
}

// perfWant fails the fixture when a search answered wrongly: a fixture that
// measures the cost of a wrong answer proves nothing about the right one.
func perfWant(tb testing.TB, name string, got, want int) {
	tb.Helper()
	//: a wrong answer fails the fixture, so the allocation count is of a correct search.
	if got != want {
		tb.Fatalf("%s = %d, want %d", name, got, want)
	}
}

// perfIndexByte2 is IndexByte2's fixture: a hit on the second byte and a
// miss, short and long.
func perfIndexByte2(tb testing.TB) func() {
	short, long := perfInputs()
	perfWant(tb, "IndexByte2(short)", swar.IndexByte2(short, '\r', '\n'), 11)
	perfWant(tb, "IndexByte2(long)", swar.IndexByte2(long, '\r', '\n'), 99)
	perfWant(tb, "IndexByte2(long, miss)", swar.IndexByte2(long, 'b', 'c'), -1)
	return func() {
		perfInt = swar.IndexByte2(short, '\r', '\n')
		perfInt = swar.IndexByte2(long, '\r', '\n')
		perfInt = swar.IndexByte2(long, 'b', 'c')
	}
}

// perfIndexLess is IndexLess's fixture: a control byte found and not found,
// short and long, and a bound past 128, which takes the byte loop.
func perfIndexLess(tb testing.TB) func() {
	short, long := perfInputs()
	perfWant(tb, "IndexLess(short)", swar.IndexLess(short, 0x20), 11)
	perfWant(tb, "IndexLess(long)", swar.IndexLess(long, 0x20), 99)
	perfWant(tb, "IndexLess(long, miss)", swar.IndexLess(long, 0x10), 99)
	perfWant(tb, "IndexLess(long, 0)", swar.IndexLess(long, 0), -1)
	perfWant(tb, "IndexLess(long, 200)", swar.IndexLess(long, 200), 0)
	return func() {
		perfInt = swar.IndexLess(short, 0x20)
		perfInt = swar.IndexLess(long, 0x20)
		perfInt = swar.IndexLess(long, 0)
		perfInt = swar.IndexLess(long, 200)
	}
}

// perfNewSet is NewSet's fixture: a set of every part, and a set with a
// bound past 128.
func perfNewSet(tb testing.TB) func() {
	perfWant(tb, "NewSet(...).Index", swar.NewSet('"', '\\', 0x20, true).Index([]byte("ab\n")), 2)
	return func() {
		perfSet = swar.NewSet('"', '\\', 0x20, true)
		perfSet = swar.NewSet('a', 'a', 200, false)
	}
}

// perfSetIndex is Set.Index's fixture: the escaping set over a short and a
// long slice, found and not found, and a set whose bound takes the byte loop.
func perfSetIndex(tb testing.TB) func() {
	short, long := perfInputs()
	escaping, wide := swar.NewSet('"', '\\', 0x20, true), swar.NewSet('b', 'b', 200, false)
	perfWant(tb, "Set.Index(short)", escaping.Index(short), 11)
	perfWant(tb, "Set.Index(long)", escaping.Index(long), 99)
	perfWant(tb, "Set.Index(wide)", wide.Index(long), 0)
	return func() {
		perfInt = escaping.Index(short)
		perfInt = escaping.Index(long)
		perfInt = wide.Index(long)
	}
}
