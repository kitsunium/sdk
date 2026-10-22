package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// checkReadAnsweredByTheLeaderOnly: a follower takes the answer to its read
// from the leader it asked, never from another peer of the term (#510).
func checkReadAnsweredByTheLeaderOnly(mut mutation) error {
	f := newTest(2, testState{term: 2, commit: 0}, mut, false)
	at := t0.Add(5 * time.Second)
	f.Step(at, msg(core.MsgHeartbeat, 1, 2, 2))
	f.Ready()
	f.ReadIndex(at, 7, true)
	f.Ready()
	resp := func(from core.NodeID, index uint64) []ReadState {
		m := msg(core.MsgReadIndexResp, from, 2, 2)
		m.Context, m.Index = 7, index
		f.Step(at, m)
		return f.Ready().ReadStates
	}
	if rs := resp(3, 99); len(rs) != 0 {
		return fmt.Errorf("a read was answered by peer 3, not by its leader 1: %+v", rs)
	}
	if rs := resp(1, 4); len(rs) != 1 || rs[0].Index != 4 {
		return fmt.Errorf("the leader's answer was not taken: %+v", rs)
	}
	return nil
}

func TestAReadIsAnsweredByItsLeaderOnly(t *testing.T) {
	if err := checkReadAnsweredByTheLeaderOnly(0); err != nil {
		t.Fatal(err)
	}
}
