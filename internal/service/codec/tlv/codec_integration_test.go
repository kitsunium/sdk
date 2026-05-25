//go:build !race

package tlv_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/tlv"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes.
var allocSink any

// TestAllocBudget pins TLV's per-call allocation ceilings for Marshal,
// Unmarshal, and (when implemented) Append over a scalar int64 — the typed
// fast-path Phases 6-8 optimised. Carries //go:build !race
// (testing.AllocsPerRun is +1 under -race) and no t.Parallel (AllocsPerRun
// reads a process-global counter). Budgets are ceilings — re-pin with intent
// on a Go toolchain bump.
func TestAllocBudget(t *testing.T) {
	payload := int64(-64_000_000_000)
	c := tlv.New()
	seed, err := c.Marshal(payload)
	if err != nil {
		t.Fatalf("seed Marshal: %v", err)
	}
	type tc struct {
		name string
		ceil float64
		fn   func()
	}
	//: ceilings captured on the race-off lane at the scratch-migration commit.
	tests := []tc{
		{"marshal", 3, func() {
			out, merr := c.Marshal(payload)
			if merr != nil {
				t.Fatalf("Marshal: %v", merr)
			}
			allocSink = out
		}},
		{"unmarshal", 3, func() {
			var dst int64
			if uerr := c.Unmarshal(seed, &dst); uerr != nil {
				t.Fatalf("Unmarshal: %v", uerr)
			}
			allocSink = dst
		}},
	}
	//: Append is optional — measure it only when the codec implements it.
	if appender, ok := c.(corecodec.Appender); ok {
		dst := make([]byte, 0, 256)
		tests = append(tests, tc{"append", 1, func() {
			out, aerr := appender.Append(dst[:0], payload)
			if aerr != nil {
				t.Fatalf("Append: %v", aerr)
			}
			allocSink = out
		}})
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := testing.AllocsPerRun(100, tc.fn)
		t.Logf("%s: allocs/op=%.0f (ceil %.0f)", tc.name, got, tc.ceil)
		if got > tc.ceil {
			t.Errorf("%s: allocs/op=%.0f > ceil %.0f", tc.name, got, tc.ceil)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
