//go:build !race

package ndjson_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/ndjson"
)

// Batch sizes the slope probe differences. Far enough apart that a one-alloc
// rounding at either end cannot fake or hide a per-record allocation.
const (
	allocSlopeLo int = 4
	allocSlopeHi int = 12
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes.
var allocSink any

// allocRec is the fixed record shape the budget cases round-trip.
type allocRec struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestAllocBudget pins ndjson's per-call allocation ceilings for Marshal,
// Unmarshal, and Append over a small fixed record batch. Carries
// //go:build !race (testing.AllocsPerRun is +1 under -race) and no
// t.Parallel (AllocsPerRun reads a process-global counter). Budgets are
// ceilings, not exact — re-pin with intent on a toolchain bump.
func TestAllocBudget(t *testing.T) {
	recs := []allocRec{{Name: "a", Age: 1}, {Name: "b", Age: 2}, {Name: "c", Age: 3}}
	c := ndjson.New()
	appender, ok := c.(corecodec.Appender)
	//: Append is the optional Appender extension — assert it once, here,
	//: rather than letting every case re-discover a nil interface.
	if !ok {
		t.Fatalf("ndjson does not implement codec.Appender")
	}
	//: pre-encode once for the Unmarshal + Append cases.
	seed, err := c.Marshal(recs)
	//: a broken seed would make every later ceiling meaningless.
	if err != nil {
		t.Fatalf("seed Marshal: %v", err)
	}
	//: reused caller buffer for the Append case — pre-sized so Append never
	//: grows it inside the probe (we measure Append's own allocs, not dst).
	appendDst := make([]byte, 0, 256)
	type tc struct {
		name string
		ceil float64
		fn   func()
	}
	//: Ceilings captured on the race-off lane over this 3-record batch.
	//: History, because it is the whole point of these two numbers:
	//:   go1.26.x  marshal 9  append 8   (2 allocs per record)
	//:   go1.27.0  marshal 12 append 11  (3 allocs per record) — the toolchain
	//:             regression of #128: encoding/json is now the json/v2
	//:             implementation (GOEXPERIMENT=jsonv2 entered the baseline in
	//:             internal/buildcfg/exp.go for 1.27), and json/v2's
	//:             marshalEncode shallow-copies every NON-pointer argument
	//:             through reflect.New to get an addressable value
	//:             (encoding/json/v2/arshal.go).
	//:   now       marshal 6  append 5   (1 alloc per record) — reclaimed by
	//:             elemForMarshal handing json the ELEMENT'S ADDRESS instead
	//:             of a copy, which skips that reflect.New and costs nothing
	//:             to box. Below the go1.26 figure, and identical on go1.26.8
	//:             and go1.27.0, so these two numbers no longer move with the
	//:             toolchain.
	//: Unmarshal is untouched by all of the above (measures 5, ceiling 17).
	tests := []tc{
		{"marshal", 6, func() { allocSink = mustMarshal(t, c, recs) }},
		{"unmarshal", 17, func() { allocSink = mustUnmarshal(t, c, seed) }},
		{"append", 5, func() { allocSink = mustAppend(t, appender, appendDst[:0], recs) }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := testing.AllocsPerRun(100, tc.fn)
		//: log the measured floor so budget tightening is data-driven.
		t.Logf("%s: allocs/op=%.0f (ceil %.0f)", tc.name, got, tc.ceil)
		//: a ceiling is only a gate if crossing it fails the build.
		if got > tc.ceil {
			t.Errorf("%s: allocs/op=%.0f > ceil %.0f", tc.name, got, tc.ceil)
		}
	}
	//: sub-tests keep each budget's verdict individually readable.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// TestAllocPerRecord pins the SLOPE rather than the total: how many
// allocations each additional record costs. This is the quantity #128 is
// actually about — "a 10 000-record NDJSON batch now allocates 10 000 more
// times than it did on 1.26" — and a fixed-size batch ceiling states it only
// indirectly, mixed in with constant per-call overhead.
//
// Measured by differencing two batch sizes, which cancels that constant term
// exactly. The table varies record SHAPE, not record count, because a probe
// that only scales one shape is unanimous and blind: []*T was ALREADY at one
// allocation per record before the fix (it hands json a pointer by
// construction) and []any is still at two (the dynamic value inside an
// interface is not addressable, so json/v2's reflect.New copy is unavoidable
// there). Only a shape-varied table shows that.
//
// Per-record figures on go1.27.0, before -> after elemForMarshal:
//
//	plain []T      3 -> 1      nested []T   3 -> 1      []string  3 -> 1
//	[]*T           1 -> 1      []any        2 -> 2
func TestAllocPerRecord(t *testing.T) {
	c := ndjson.New()
	appender, ok := c.(corecodec.Appender)
	//: Append shares the same per-record loop, so the slope must be gated on
	//: both — a missing Appender means half this test measures nothing.
	if !ok {
		t.Fatalf("ndjson does not implement codec.Appender")
	}
	//: pre-sized so Append's own growth never pollutes the slope.
	appendDst := make([]byte, 0, 1<<16)
	type tc struct {
		name string
		ceil float64
		mk   func(n int) any
	}
	tests := []tc{
		{"plain struct", 1, allocBatchPlain},
		{"nested + unicode", 1, allocBatchNested},
		{"strings needing escapes", 1, allocBatchStrings},
		{"slice of pointers", 1, allocBatchPointers},
		//: interface elements keep json/v2's reflect.New copy — see the doc
		//: comment. Pinned at its measured value so a THIRD allocation would
		//: still trip the gate.
		{"interface elements", 2, allocBatchAny},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		perRecMarshal := allocSlope(tc.mk, func(v any) float64 {
			return testing.AllocsPerRun(100, func() { allocSink = mustMarshal(t, c, v) })
		})
		perRecAppend := allocSlope(tc.mk, func(v any) float64 {
			return testing.AllocsPerRun(100, func() { allocSink = mustAppend(t, appender, appendDst[:0], v) })
		})
		t.Logf("%s: marshal %.3f allocs/record, append %.3f allocs/record (ceil %.0f)",
			tc.name, perRecMarshal, perRecAppend, tc.ceil)
		//: Marshal and Append run the same loop, so both must hold the ceiling
		//: — reporting only one would let the other drift unobserved.
		if perRecMarshal > tc.ceil {
			t.Errorf("%s: Marshal %.3f allocs/record > ceil %.0f", tc.name, perRecMarshal, tc.ceil)
		}
		//: Append writes into the caller's buffer where Marshal detaches its
		//: own, so the two can drift apart — check the second explicitly.
		if perRecAppend > tc.ceil {
			t.Errorf("%s: Append %.3f allocs/record > ceil %.0f", tc.name, perRecAppend, tc.ceil)
		}
	}
	//: one sub-test per SHAPE, so a shape-specific regression names itself.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// allocSlope returns allocations per record: the difference between two batch
// sizes divided by the gap, which cancels the constant per-call term exactly.
// probe measures one batch; mk builds a batch of n records of a single shape.
func allocSlope(mk func(n int) any, probe func(v any) float64) float64 {
	//: build both batches before measuring so neither construction lands
	//: inside the other's AllocsPerRun window.
	small, large := mk(allocSlopeLo), mk(allocSlopeHi)
	//: the gap is a compile-time constant, so this is the slope, not a ratio.
	return (probe(large) - probe(small)) / float64(allocSlopeHi-allocSlopeLo)
}

// mustMarshal encodes v and fails the test on error, so the AllocsPerRun
// callbacks stay one expression long.
func mustMarshal(t *testing.T, c corecodec.Codec, v any) []byte {
	t.Helper()
	out, err := c.Marshal(v)
	//: an error inside the probe would silently distort the alloc count.
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return out
}

// mustAppend appends v onto dst and fails the test on error.
func mustAppend(t *testing.T, ap corecodec.Appender, dst []byte, v any) []byte {
	t.Helper()
	out, err := ap.Append(dst, v)
	//: same reason as mustMarshal — a failed probe measures nothing.
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return out
}

// mustUnmarshal decodes data into a fresh slice and fails the test on error.
func mustUnmarshal(t *testing.T, c corecodec.Codec, data []byte) []allocRec {
	t.Helper()
	var dst []allocRec
	//: decode into a fresh slice each call — reusing one would hide the
	//: allocation the budget exists to count.
	if err := c.Unmarshal(data, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return dst
}

// allocBatchPlain builds n plain structs — the shape the budget batch uses.
func allocBatchPlain(n int) any {
	out := make([]allocRec, n)
	//: vary a field so the encoder cannot fold the records.
	for i := range out {
		out[i] = allocRec{Name: "a", Age: i}
	}
	return out
}

// allocBatchNested builds n records with an embedded pointer, a slice field
// and non-ASCII text — a shape the flat {name,age} batch never reaches.
func allocBatchNested(n int) any {
	out := make([]nested, n)
	//: each record carries its own Inner pointer, not a shared one.
	for i := range out {
		out[i] = nested{ID: "é中", Tags: []string{"t"}, Inner: &payload{Name: "z", Age: i}}
	}
	return out
}

// allocBatchStrings builds n strings that all need JSON escaping, so the
// encoder takes its escaping path rather than the memcpy one.
func allocBatchStrings(n int) any {
	out := make([]string, n)
	//: newline, quote and U+2028 each drive a different escape branch.
	for i := range out {
		out[i] = "a\nb\"q\" "
	}
	return out
}

// allocBatchPointers builds n POINTER elements — the control case. It hands
// encoding/json a pointer by construction, so it never paid json/v2's
// addressability copy and must measure the same before and after the fix.
func allocBatchPointers(n int) any {
	out := make([]*allocRec, n)
	//: distinct allocations, so no element aliases another.
	for i := range out {
		out[i] = &allocRec{Name: "a", Age: i}
	}
	return out
}

// allocBatchAny builds n interface elements, whose dynamic value is not
// addressable — the shape whose per-record cost the fix cannot reclaim.
func allocBatchAny(n int) any {
	out := make([]any, n)
	//: an int in an interface: small, and still not addressable.
	for i := range out {
		out[i] = i
	}
	return out
}
