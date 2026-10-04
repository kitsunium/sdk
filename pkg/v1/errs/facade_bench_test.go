// Benchmarks for every name this facade forwards to internal/kernel/errs: the
// eighteen accessors, predicates, code helpers and Field builders a consumer
// calls through pkg/v1/errs. Each prices ONE call made from a consumer's own
// package, which is the cost a re-export adds or removes, so the before/after
// table in BENCH.md compares the same calls under the two shapes a re-export can
// take — a function variable and a forwarding function.
//
// Every argument is read from a field of a package-level variable and every
// result written to a package-level sink: the compiler can neither fold a call
// whose arguments it could prove constant nor drop one whose result nobody
// reads.
//
// Run: `cd pkg && GOWORK=off go test -run='^$' -bench=Facade -count=10 ./v1/errs`
package errs_test

import (
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// facadeArgs is every argument the forwarded calls take.
type facadeArgs struct {
	// code is the application-range code HasCode looks for.
	code errs.Code
	// mask is the prefix mask NewPrefixMatcher is given.
	mask errs.Code
	// reason is the reason HasReason looks for.
	reason string
	// text is the dotted quad ParseCode reads.
	text string
	// major, layer, pkg and serial are the octets Pack is given.
	major  errs.Major
	layer  errs.Layer
	pkg    errs.PkgCode
	serial errs.Serial
	// key and the fields below it are what the Field builders take.
	key   string
	str   string
	num   int
	num64 int64
	flag  bool
	float float64
}

// facadeIn holds the arguments. It is a variable, read on every iteration: an
// argument the compiler could prove constant would let an inlined forwarder
// fold its call away, and the benchmark would time nothing.
var facadeIn = facadeArgs{
	code: errs.Pack(0x40, 1, 1, 1), mask: errs.MaskByPackage,
	reason: "BENCH_REASON", text: "64.1.1.1",
	major: 0x40, layer: 1, pkg: 1, serial: 1,
	key: "key", str: "value", num: 42, num64: 42, flag: true, float: 4.2,
}

// errFacade is a typed SDK error behind one stdlib wrap — the chain an accessor
// walks on the common path.
var errFacade = fmt.Errorf("op: %w", errs.New(facadeIn.code, facadeIn.reason,
	"bench public", "bench private", errs.String("problem", "bench")))

// The sinks every forwarded call writes.
var (
	facadeCodeSink    errs.Code           //nolint:unused // bench sink
	facadeOKSink      bool                //nolint:unused // bench sink
	facadeStrSink     string              //nolint:unused // bench sink
	facadeIntSink     int                 //nolint:unused // bench sink
	facadeFieldsSink  []errs.Field        //nolint:unused // bench sink
	facadeFieldSink   errs.Field          //nolint:unused // bench sink
	facadeMatcherSink *errs.PrefixMatcher //nolint:unused // bench sink
	facadeErrSink     error               //nolint:errcheck // bench sink
)

// BenchmarkFacade_CodeOf prices CodeOf on a typed chain.
func BenchmarkFacade_CodeOf(b *testing.B) {
	for b.Loop() {
		facadeCodeSink, facadeOKSink = errs.CodeOf(errFacade)
	}
}

// BenchmarkFacade_ReasonOf prices ReasonOf on a typed chain.
func BenchmarkFacade_ReasonOf(b *testing.B) {
	for b.Loop() {
		facadeStrSink, facadeOKSink = errs.ReasonOf(errFacade)
	}
}

// BenchmarkFacade_PublicOf prices PublicOf on a typed chain.
func BenchmarkFacade_PublicOf(b *testing.B) {
	for b.Loop() {
		facadeStrSink = errs.PublicOf(errFacade)
	}
}

// BenchmarkFacade_PrivateOf prices PrivateOf on a typed chain.
func BenchmarkFacade_PrivateOf(b *testing.B) {
	for b.Loop() {
		facadeStrSink = errs.PrivateOf(errFacade)
	}
}

// BenchmarkFacade_HTTPStatusOf prices HTTPStatusOf on a typed chain.
func BenchmarkFacade_HTTPStatusOf(b *testing.B) {
	for b.Loop() {
		facadeIntSink = errs.HTTPStatusOf(errFacade)
	}
}

// BenchmarkFacade_ExitCodeOf prices ExitCodeOf on a typed chain.
func BenchmarkFacade_ExitCodeOf(b *testing.B) {
	for b.Loop() {
		facadeIntSink = errs.ExitCodeOf(errFacade)
	}
}

// BenchmarkFacade_FieldsOf prices FieldsOf on a typed chain, its defensive
// copy included.
func BenchmarkFacade_FieldsOf(b *testing.B) {
	for b.Loop() {
		facadeFieldsSink = errs.FieldsOf(errFacade)
	}
}

// BenchmarkFacade_HasCode prices HasCode finding the code on a typed chain.
func BenchmarkFacade_HasCode(b *testing.B) {
	for b.Loop() {
		facadeOKSink = errs.HasCode(errFacade, facadeIn.code)
	}
}

// BenchmarkFacade_HasReason prices HasReason finding the reason on a typed
// chain.
func BenchmarkFacade_HasReason(b *testing.B) {
	for b.Loop() {
		facadeOKSink = errs.HasReason(errFacade, facadeIn.reason)
	}
}

// BenchmarkFacade_NewPrefixMatcher prices building a matcher, its allocation
// included.
func BenchmarkFacade_NewPrefixMatcher(b *testing.B) {
	for b.Loop() {
		facadeMatcherSink = errs.NewPrefixMatcher(facadeIn.code, facadeIn.mask)
	}
}

// BenchmarkFacade_Pack prices packing four octets read at run time.
func BenchmarkFacade_Pack(b *testing.B) {
	for b.Loop() {
		facadeCodeSink = errs.Pack(facadeIn.major, facadeIn.layer, facadeIn.pkg, facadeIn.serial)
	}
}

// BenchmarkFacade_ParseCode prices reading a dotted quad.
func BenchmarkFacade_ParseCode(b *testing.B) {
	for b.Loop() {
		facadeCodeSink, facadeErrSink = errs.ParseCode(facadeIn.text)
	}
}

// BenchmarkFacade_String prices building a string Field.
func BenchmarkFacade_String(b *testing.B) {
	for b.Loop() {
		facadeFieldSink = errs.String(facadeIn.key, facadeIn.str)
	}
}

// BenchmarkFacade_Int prices building an int Field.
func BenchmarkFacade_Int(b *testing.B) {
	for b.Loop() {
		facadeFieldSink = errs.Int(facadeIn.key, facadeIn.num)
	}
}

// BenchmarkFacade_Int64 prices building an int64 Field.
func BenchmarkFacade_Int64(b *testing.B) {
	for b.Loop() {
		facadeFieldSink = errs.Int64(facadeIn.key, facadeIn.num64)
	}
}

// BenchmarkFacade_Bool prices building a bool Field.
func BenchmarkFacade_Bool(b *testing.B) {
	for b.Loop() {
		facadeFieldSink = errs.Bool(facadeIn.key, facadeIn.flag)
	}
}

// BenchmarkFacade_Float prices building a float Field.
func BenchmarkFacade_Float(b *testing.B) {
	for b.Loop() {
		facadeFieldSink = errs.Float(facadeIn.key, facadeIn.float)
	}
}

// BenchmarkFacade_NewFieldValue prices the New-prefixed string Field builder.
func BenchmarkFacade_NewFieldValue(b *testing.B) {
	for b.Loop() {
		facadeFieldSink = errs.NewFieldValue(facadeIn.key, facadeIn.str)
	}
}
