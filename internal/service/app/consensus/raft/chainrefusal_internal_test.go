package raft

import (
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// TestAChainRefusalIsReportedToTheDriver pins what D13 counts as
// consensus_chain_refusals_total: every append a follower refuses for a
// broken chain names, in the Ready, the leader it refused, so the driver can
// count it by leader. Before, the refusal reached Health only as the latest
// run at one index, and the counter the ADR names was never emitted.
func TestAChainRefusalIsReportedToTheDriver(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 0}, 0, false)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Entries = chainEntries(0, 1, 1)
	m.Entries[0].ChainSum++ // a chain the follower cannot verify
	f.Step(t0, m)
	rd := f.Ready()
	if len(rd.ChainRefusedBy) != 1 || rd.ChainRefusedBy[0] != 1 {
		t.Fatalf("ChainRefusedBy = %v, want [1]", rd.ChainRefusedBy)
	}
}
