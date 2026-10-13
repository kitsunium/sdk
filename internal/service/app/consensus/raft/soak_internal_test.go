package raft

import (
	"os"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// TestSimSoak is the long lane of D15, run with CONSENSUS_SIM_SOAK=1.
func TestSimSoak(t *testing.T) {
	if os.Getenv("CONSENSUS_SIM_SOAK") == "" {
		t.Skip()
	}
	for seed := uint64(1); seed <= 600; seed++ {
		s := newSim(simOptions{seed: seed, drop: 0.05, dup: 0.02, crashRate: 0.5, diskMax: 20 * time.Millisecond})
		for k := 0; k < 6 && s.err == nil; k++ {
			switch k % 3 {
			case 0:
				s.partition([]core.NodeID{1}, []core.NodeID{2, 3})
			case 1:
				s.partition([]core.NodeID{1, 2}, []core.NodeID{3})
			default:
				s.heal()
			}
			s.run(s.after(5*time.Second), true)
		}
		if s.err != nil {
			t.Errorf("seed %d: %v", seed, s.err)
		}
		// The snapshot schedule: cuts, installs, restarts with no table.
		if s := snapSim(seed, 0); s.err != nil {
			t.Errorf("snapshots, seed %d: %v", seed, s.err)
		}
	}
}
