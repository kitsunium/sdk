//go:build !race

package form_test

import (
	"net/url"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/form"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes.
var allocSink any

// TestAllocBudget pins form's per-call allocation ceilings for Marshal,
// Unmarshal, and Append over a small fixed body. Carries //go:build !race
// (testing.AllocsPerRun is +1 under -race) and no t.Parallel (AllocsPerRun
// reads a process-global counter). The package is covered by the race-off
// alloc lane through the `//internal/service/codec/...` entry in
// tools/alloc-lane-targets.txt — CLAUDE.md rule 12 wants that lane named
// wherever a `!race` file is added, and this is the naming.
//
// Budgets are ceilings, not exact figures — re-pin with intent on a
// toolchain bump.
func TestAllocBudget(t *testing.T) {
	//: a small, realistic form: three keys, one of them repeated.
	values := url.Values{"name": {"ada"}, "tag": {"x", "y"}, "n": {"36"}}
	c := form.New()
	//: Append is the optional Appender extension — assert it once.
	appender, ok := c.(corecodec.Appender)
	if !ok {
		t.Fatalf("form does not implement codec.Appender")
	}
	//: pre-encode once for the Unmarshal case.
	seed, err := c.Marshal(values)
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
	//: Measured on the race-off lane (go1.27, this 3-key/4-value body):
	//: marshal 3, unmarshal 8, append 2. Marshal = sorted key slice + the
	//: single pre-sized output buffer + the allocSink interface box; Append
	//: writes into the caller's buffer so it drops the output one. Unmarshal
	//: is dominated by url.ParseQuery — the string(data) copy plus the map
	//: and its per-key slices. Ceilings carry one allocation of headroom so
	//: a toolchain nudge does not fail the lane on noise.
	tests := []tc{
		{"marshal", 4, func() {
			out, merr := c.Marshal(values)
			if merr != nil {
				t.Fatalf("Marshal: %v", merr)
			}
			allocSink = out
		}},
		{"unmarshal", 10, func() {
			var dst url.Values
			if uerr := c.Unmarshal(seed, &dst); uerr != nil {
				t.Fatalf("Unmarshal: %v", uerr)
			}
			allocSink = dst
		}},
		{"append", 3, func() {
			out, aerr := appender.Append(appendDst[:0], values)
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
