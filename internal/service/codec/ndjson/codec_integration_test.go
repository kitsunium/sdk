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
	//: ceilings captured on the race-off lane at the scratch-migration commit.
	tests := []tc{
		{"marshal", 9, func() {
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
		{"append", 8, func() {
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
