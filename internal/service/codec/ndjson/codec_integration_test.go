//go:build !race

package ndjson_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/ndjson"
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
	//: Append is the optional Appender extension — assert it once.
	appender, ok := c.(corecodec.Appender)
	if !ok {
		t.Fatalf("ndjson does not implement codec.Appender")
	}
	//: pre-encode once for the Unmarshal + Append cases.
	seed, err := c.Marshal(recs)
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
		{"marshal", 6, func() {
			out, merr := c.Marshal(recs)
			if merr != nil {
				t.Fatalf("Marshal: %v", merr)
			}
			allocSink = out
		}},
		{"unmarshal", 17, func() {
			var dst []allocRec
			if uerr := c.Unmarshal(seed, &dst); uerr != nil {
				t.Fatalf("Unmarshal: %v", uerr)
			}
			allocSink = dst
		}},
		{"append", 5, func() {
			out, aerr := appender.Append(appendDst[:0], recs)
			if aerr != nil {
				t.Fatalf("Append: %v", aerr)
			}
			allocSink = out
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := testing.AllocsPerRun(100, tc.fn)
		//: log the measured floor so budget tightening is data-driven.
		t.Logf("%s: allocs/op=%.0f (ceil %.0f)", tc.name, got, tc.ceil)
		if got > tc.ceil {
			t.Errorf("%s: allocs/op=%.0f > ceil %.0f", tc.name, got, tc.ceil)
		}
	}
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
	//: Append shares the same per-record loop — gate both.
	appender, ok := c.(corecodec.Appender)
	if !ok {
		t.Fatalf("ndjson does not implement codec.Appender")
	}
	//: pre-sized so Append's own growth never pollutes the slope.
	appendDst := make([]byte, 0, 1<<16)
	//: two sizes far enough apart that a 1-alloc rounding cannot fake a slope.
	const lo, hi int = 4, 12
	type tc struct {
		name string
		ceil float64
		mk   func(n int) any
	}
	tests := []tc{
		{"plain struct", 1, func(n int) any {
			out := make([]allocRec, n)
			for i := range out {
				out[i] = allocRec{Name: "a", Age: i}
			}
			return out
		}},
		{"nested + unicode", 1, func(n int) any {
			out := make([]nested, n)
			for i := range out {
				out[i] = nested{ID: "é中", Tags: []string{"t"}, Inner: &payload{Name: "z", Age: i}}
			}
			return out
		}},
		{"strings needing escapes", 1, func(n int) any {
			out := make([]string, n)
			for i := range out {
				out[i] = "a\nb\"q\" "
			}
			return out
		}},
		{"slice of pointers", 1, func(n int) any {
			out := make([]*allocRec, n)
			for i := range out {
				out[i] = &allocRec{Name: "a", Age: i}
			}
			return out
		}},
		//: interface elements keep json/v2's reflect.New copy — see the doc
		//: comment. Pinned at its measured value so a THIRD allocation would
		//: still trip the gate.
		{"interface elements", 2, func(n int) any {
			out := make([]any, n)
			for i := range out {
				out[i] = i
			}
			return out
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: one closure per (operation, batch size); AllocsPerRun is serial.
		marshalAt := func(v any) float64 {
			return testing.AllocsPerRun(100, func() {
				out, merr := c.Marshal(v)
				if merr != nil {
					t.Fatalf("%s: Marshal: %v", tc.name, merr)
				}
				allocSink = out
			})
		}
		appendAt := func(v any) float64 {
			return testing.AllocsPerRun(100, func() {
				out, aerr := appender.Append(appendDst[:0], v)
				if aerr != nil {
					t.Fatalf("%s: Append: %v", tc.name, aerr)
				}
				allocSink = out
			})
		}
		small, large := tc.mk(lo), tc.mk(hi)
		//: differencing cancels the per-call constant, leaving the slope.
		perRecMarshal := (marshalAt(large) - marshalAt(small)) / float64(hi-lo)
		perRecAppend := (appendAt(large) - appendAt(small)) / float64(hi-lo)
		t.Logf("%s: marshal %.3f allocs/record, append %.3f allocs/record (ceil %.0f)",
			tc.name, perRecMarshal, perRecAppend, tc.ceil)
		if perRecMarshal > tc.ceil {
			t.Errorf("%s: Marshal %.3f allocs/record > ceil %.0f", tc.name, perRecMarshal, tc.ceil)
		}
		if perRecAppend > tc.ceil {
			t.Errorf("%s: Append %.3f allocs/record > ceil %.0f", tc.name, perRecAppend, tc.ceil)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
