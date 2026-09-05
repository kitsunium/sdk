// Package net_test — the server lifecycle phase.
package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_Phase_String pins the token for every declared phase and for values
// outside the block. A readiness probe reads these strings, so a phase that
// rendered as an empty string would look like "no answer" rather than like a
// server that is still binding.
func Test_Phase_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		phase corenet.Phase
		want  string
	}
	tests := []tc{
		{"constructed but unbound", corenet.PhaseNew, "new"},
		{"binding listeners", corenet.PhaseStarting, "starting"},
		{"bound and accepting", corenet.PhaseServing, "serving"},
		{"no longer accepting", corenet.PhaseDraining, "draining"},
		{"fully released", corenet.PhaseStopped, "stopped"},
		//: an out-of-range value is reported, never hidden behind an empty
		//: string a probe would read as a missing field.
		{"one past the last phase", corenet.PhaseStopped + 1, "unknown"},
		{"a far out-of-range value", corenet.Phase(200), "unknown"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.phase.String(); got != c.want {
			t.Errorf("Phase(%d).String() = %q, want %q", uint8(c.phase), got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the zero value must be the first phase, so a freshly constructed State
	//: reports "new" without anyone having to set it.
	if corenet.PhaseNew != 0 {
		t.Errorf("PhaseNew = %d, want the zero value", uint8(corenet.PhaseNew))
	}
	//: the phases must stay ordered as the lifecycle runs; a reordered block
	//: would silently invert any "at least serving" comparison.
	ordered := []corenet.Phase{
		corenet.PhaseNew, corenet.PhaseStarting, corenet.PhaseServing,
		corenet.PhaseDraining, corenet.PhaseStopped,
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1] >= ordered[i] {
			t.Errorf("%v is not before %v", ordered[i-1], ordered[i])
		}
	}
}
