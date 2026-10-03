package trace_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// The four attribute constructors this facade forwards to the shared model,
// priced one call each from a consumer's own package: what a re-export adds or
// removes, compared in BENCH.md under the two shapes it can take — a function
// variable and a forwarding function. Every argument is read from a field of a
// package-level variable and the result written to a package-level sink, so the
// compiler can neither fold a call nor drop one.
//
// Run: `cd pkg && GOWORK=off go test -run='^$' -bench=Facade -count=10 ./v1/observe/trace`

// attrArgs is every argument the constructors take.
type attrArgs struct {
	// key is every attribute's key.
	key string
	// str, flag, num and float are the values of the four kinds.
	str   string
	flag  bool
	num   int64
	float float64
}

// attrIn holds the arguments. It is a variable, read on every iteration: an
// argument the compiler could prove constant would let an inlined constructor
// fold away, and the benchmark would time nothing.
var attrIn = attrArgs{key: "http.request.method", str: "GET", flag: true, num: 200, float: 0.25}

// attrSink receives every attribute built.
var attrSink trace.Attr

// BenchmarkFacade_String prices building a string attribute.
func BenchmarkFacade_String(b *testing.B) {
	for b.Loop() {
		attrSink = trace.String(attrIn.key, attrIn.str)
	}
}

// BenchmarkFacade_Bool prices building a bool attribute.
func BenchmarkFacade_Bool(b *testing.B) {
	for b.Loop() {
		attrSink = trace.Bool(attrIn.key, attrIn.flag)
	}
}

// BenchmarkFacade_Int64 prices building an integer attribute.
func BenchmarkFacade_Int64(b *testing.B) {
	for b.Loop() {
		attrSink = trace.Int64(attrIn.key, attrIn.num)
	}
}

// BenchmarkFacade_Float64 prices building a double attribute.
func BenchmarkFacade_Float64(b *testing.B) {
	for b.Loop() {
		attrSink = trace.Float64(attrIn.key, attrIn.float)
	}
}
