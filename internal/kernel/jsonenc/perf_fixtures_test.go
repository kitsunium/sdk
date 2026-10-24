//go:build !race

// The fixtures of jsonenc's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget.
//
// The claim is that an appender allocates nothing once the destination has the
// room: the shapes that lose it are a string converted to a slice for the byte
// search, a closure over the destination, and an Object that escapes. Every
// functional test passes against any of them. Each fixture's call walks the
// three paths of a string — plain ASCII, ASCII with escapes, and non-ASCII
// with a bad byte and a separator — on a destination that has the capacity.
//
// The `!race` constraint is not a preference: the race detector allocates on
// every access, so a malloc count under `-race` measures the detector.
package jsonenc_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/jsonenc"
)

// perfSink keeps each call's result alive.
var perfSink []byte

// perfStrings are the three paths a string takes: plain, escaped, and one with
// non-ASCII text, a bad byte and a line separator.
var perfStrings = []string{
	strings.Repeat("plain ascii text ", 8),
	"a \"quoted\" value\\with\nescapes\tand\x01controls past the first word",
	"déploiement \xff terminé \u2028 東京 ✓",
}

// perfAppendString is AppendString's fixture.
func perfAppendString(tb testing.TB) func() {
	dst := make([]byte, 0, 1024)
	return func() {
		for _, s := range perfStrings {
			perfSink = jsonenc.AppendString(dst[:0], s)
		}
	}
}

// perfAppendEscaped is AppendEscaped's fixture.
func perfAppendEscaped(tb testing.TB) func() {
	dst := make([]byte, 0, 1024)
	return func() {
		for _, s := range perfStrings {
			perfSink = jsonenc.AppendEscaped(dst[:0], s)
		}
	}
}

// perfAppendInt is AppendInt's fixture.
func perfAppendInt(tb testing.TB) func() {
	dst := make([]byte, 0, 64)
	return func() {
		perfSink = jsonenc.AppendInt(dst[:0], -9223372036854775808)
	}
}

// perfAppendUint is AppendUint's fixture.
func perfAppendUint(tb testing.TB) func() {
	dst := make([]byte, 0, 64)
	return func() {
		perfSink = jsonenc.AppendUint(dst[:0], 18446744073709551615)
	}
}

// perfBeginObject is BeginObject's fixture: a whole object, every member kind,
// since the object is the thing that must not reach the heap.
func perfBeginObject(tb testing.TB) func() {
	dst := make([]byte, 0, 1024)
	raw := []byte(`{"id":1}`)
	return func() {
		perfSink = jsonenc.BeginObject(dst[:0]).
			String("level", "info").
			String("msg", perfStrings[1]).
			Int("n", -7).
			Uint("u", 7).
			Bool("ok", true).
			Null("err").
			Raw("nested", raw).
			End()
	}
}
