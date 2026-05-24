//go:build !race

package baseenc_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/baseenc"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes — the
// encoded output is parked here so the compiler cannot prove the call has
// no observable effect.
var allocSink any

// allocPayload is the fixed, JSON-trivial value every budget case encodes.
// Small enough that the measured allocs/op reflect the codec's own per-call
// overhead (pooled JSON buffer rent + base-N output), not payload size.
var allocPayload = map[string]int{"a": 1, "b": 2, "c": 3}

// TestAllocBudget pins the per-call allocation ceiling for each base-N
// variant's Marshal and Append. Carries //go:build !race because
// testing.AllocsPerRun reports one extra alloc under the race detector, so
// the budgets are only meaningful race-off (the `alloc` Bazel lane). Budgets
// are ceilings (`<=`), not exact: a Go toolchain bump that shifts inlining by
// ±1 alloc must not flake the gate — re-pin with intent if a bump moves the
// floor. No t.Parallel: AllocsPerRun reads a process-global counter, so
// concurrent subtests would corrupt each other's measurements.
func TestAllocBudget(t *testing.T) {
	type tc struct {
		name        string
		c           codec.Codec
		marshalCeil float64
		appendCeil  float64
	}
	//: ceilings captured on the race-off lane at the scratch-migration commit.
	tests := []tc{
		{"base64", baseenc.Base64, 9, 8},
		{"base64url", baseenc.Base64URL, 9, 8},
		{"base32", baseenc.Base32, 9, 8},
		{"base16", baseenc.Base16, 9, 8},
		{"hex", baseenc.Hex, 9, 8},
		{"ascii85", baseenc.ASCII85, 9, 8},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: Marshal budget — fresh []byte returned each call.
		gotMarshal := testing.AllocsPerRun(100, func() {
			out, err := tc.c.Marshal(allocPayload)
			if err != nil {
				t.Fatalf("%s: Marshal err=%v", tc.name, err)
			}
			allocSink = out
		})
		if gotMarshal > tc.marshalCeil {
			t.Errorf("%s: Marshal allocs/op=%.0f > ceil %.0f", tc.name, gotMarshal, tc.marshalCeil)
		}
		//: Append budget — encode into a pre-sized caller-owned buffer.
		appender, ok := tc.c.(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement codec.Appender", tc.name)
		}
		dst := make([]byte, 0, 256)
		gotAppend := testing.AllocsPerRun(100, func() {
			out, err := appender.Append(dst, allocPayload)
			if err != nil {
				t.Fatalf("%s: Append err=%v", tc.name, err)
			}
			allocSink = out
		})
		if gotAppend > tc.appendCeil {
			t.Errorf("%s: Append allocs/op=%.0f > ceil %.0f", tc.name, gotAppend, tc.appendCeil)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
