package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestPhaseNamesEveryValue pins that each lifecycle phase renders a distinct
// token, so a status line or a readiness probe can tell "not yet listening"
// apart from "draining" — the distinction a single boolean cannot express.
func TestPhaseNamesEveryValue(t *testing.T) {
	t.Parallel()
	cases := map[corenet.Phase]string{
		corenet.PhaseNew:      "new",
		corenet.PhaseStarting: "starting",
		corenet.PhaseServing:  "serving",
		corenet.PhaseDraining: "draining",
		corenet.PhaseStopped:  "stopped",
		corenet.Phase(200):    "unknown",
	}
	seen := make(map[string]struct{}, len(cases))
	for phase, want := range cases {
		if got := phase.String(); got != want {
			t.Fatalf("Phase(%d).String() = %q, want %q", phase, got, want)
		}
		seen[want] = struct{}{}
	}
	if len(seen) != len(cases) {
		t.Fatalf("phases collide: %d distinct names for %d phases", len(seen), len(cases))
	}
}

// TestStateDegradedReportsAnyFallback pins the one-call answer to "did anything
// silently fall back?". A degradation buried in one listener of several must
// still surface, because a fallback nobody notices is the failure mode this
// field exists to prevent.
func TestStateDegradedReportsAnyFallback(t *testing.T) {
	t.Parallel()
	healthy := corenet.StateValue{Listeners: []corenet.ListenerStateValue{
		{Group: "api", Address: ":8443"},
		{Group: "dns", Address: ":53"},
	}}
	if healthy.Degraded() {
		t.Fatal("a fully healthy server reported degradation")
	}
	mixed := corenet.StateValue{Listeners: []corenet.ListenerStateValue{
		{Group: "api", Address: ":8443"},
		{Group: "dns", Address: ":53", Degraded: true, DegradedReason: "SO_REUSEPORT unavailable"},
	}}
	if !mixed.Degraded() {
		t.Fatal("a degraded listener did not surface through State")
	}
	var empty corenet.StateValue
	if empty.Degraded() {
		t.Fatal("a server with no listeners reported degradation")
	}
}
