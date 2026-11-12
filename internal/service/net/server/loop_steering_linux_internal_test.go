//go:build linux

// Package server — #857's steering rows reach State: a refused CPU is a
// capability fault, and the group serves.
package server

import (
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestLoopSteering_affinityFault pins Affinity{8191} on a host without that
// CPU: the group starts and serves, loop-affinity reads missing, and State
// reports a capability fault naming it.
func TestLoopSteering_affinityFault(t *testing.T) {
	srv := newServer(envOf(nil))
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Affinity: []int{8191}})).HandleLoop(&loopLineH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	st := srv.State()
	if src := st.Loops[0].SlabBudgetSource; src != "default" && src != "derived" {
		t.Fatalf("SlabBudgetSource = %q, want default or derived (#809)", src)
	}
	if !hasCapability(st.Loops[0].Capabilities, corenet.CapabilityLoopAffinity, statusMissing) {
		t.Fatalf("capabilities = %+v, want loop-affinity missing", st.Loops[0].Capabilities)
	}
	found := false
	for _, f := range st.Faults {
		found = found || (f.Source == faultCapability && strings.HasPrefix(f.Reason, string(corenet.CapabilityLoopAffinity)))
	}
	if !found {
		t.Fatalf("faults = %+v, want a loop-affinity capability fault", st.Faults)
	}
}
