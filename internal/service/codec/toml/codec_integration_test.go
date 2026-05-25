//go:build !race

package toml_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/toml"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes.
var allocSink any

// TestAllocBudget pins TOML's per-call allocation ceilings for Marshal,
// Unmarshal, and (when implemented) Append over a small fixed table. Carries
// //go:build !race (testing.AllocsPerRun is +1 under -race) and no
// t.Parallel (AllocsPerRun reads a process-global counter). Budgets are
// ceilings — re-pin with intent on a Go toolchain bump.
func TestAllocBudget(t *testing.T) {
	payload := map[string]int{"a": 1, "b": 2, "c": 3}
	c := toml.New()
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
		{"marshal", 17, func() {
			out, merr := c.Marshal(payload)
			if merr != nil {
				t.Fatalf("Marshal: %v", merr)
			}
			allocSink = out
		}},
		{"unmarshal", 15, func() {
			var dst map[string]int
			if uerr := c.Unmarshal(seed, &dst); uerr != nil {
				t.Fatalf("Unmarshal: %v", uerr)
			}
			allocSink = dst
		}},
	}
	//: Append is optional — measure it only when the codec implements it.
	if appender, ok := c.(corecodec.Appender); ok {
		dst := make([]byte, 0, 256)
		tests = append(tests, tc{"append", 16, func() {
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
