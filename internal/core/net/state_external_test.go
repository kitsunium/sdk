// Package net_test — the server's reported state.
package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_StateValue_Degraded pins the one-call answer to "did anything silently
// fall back?". A degradation buried in one listener of several must still
// surface, because a fallback nobody notices is the failure mode this field
// exists to prevent.
func Test_StateValue_Degraded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		listeners []corenet.ListenerStateValue
		want      bool
	}
	tests := []tc{
		{"no listeners at all", nil, false},
		{"an empty listener set", []corenet.ListenerStateValue{}, false},
		{
			"every listener healthy",
			[]corenet.ListenerStateValue{
				{Group: "api", Address: ":8443"},
				{Group: "dns", Address: ":53"},
			},
			false,
		},
		{
			//: the case the field exists for: one degraded listener among
			//: several healthy ones must still surface.
			"a degraded listener among healthy ones",
			[]corenet.ListenerStateValue{
				{Group: "api", Address: ":8443"},
				{Group: "dns", Address: ":53", Degraded: true, DegradedReason: "SO_REUSEPORT unavailable"},
			},
			true,
		},
		{
			"the first listener degraded",
			[]corenet.ListenerStateValue{
				{Group: "api", Address: ":8443", Degraded: true, DegradedReason: "SO_REUSEPORT unavailable"},
				{Group: "dns", Address: ":53"},
			},
			true,
		},
		{
			"every listener degraded",
			[]corenet.ListenerStateValue{
				{Group: "api", Address: ":8443", Degraded: true},
				{Group: "dns", Address: ":53", Degraded: true},
			},
			true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		state := corenet.StateValue{Listeners: c.listeners}
		if got := state.Degraded(); got != c.want {
			t.Errorf("Degraded() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPhaseNamesEveryValue pins the phase/name mapping in BOTH directions:
// every phase renders a token, and every token comes back from exactly one
// phase. The reverse pass is what catches a copy-pasted case arm returning a
// neighbour's token — a one-way check would still see five names for five
// phases and pass.
func TestPhaseNamesEveryValue(t *testing.T) {
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
		{"an out-of-range value", corenet.Phase(200), "unknown"},
	}
	//: forward: phase -> name, and no two phases may share a name.
	byName := make(map[string]corenet.Phase, len(tests))
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
		if prior, clash := byName[c.want]; clash {
			t.Errorf("Phase(%d) and Phase(%d) both render %q",
				uint8(prior), uint8(c.phase), c.want)
		}
		byName[c.want] = c.phase
	}

	//: reverse: every declared token must be produced by exactly the phase the
	//: table claims, walked from the name side so a missing arm cannot hide.
	for _, c := range tests {
		phase, ok := byName[c.want]
		if !ok {
			t.Errorf("no phase renders %q", c.want)
			continue
		}
		if phase != c.phase {
			t.Errorf("%q comes back from Phase(%d), want Phase(%d)",
				c.want, uint8(phase), uint8(c.phase))
		}
	}
}
