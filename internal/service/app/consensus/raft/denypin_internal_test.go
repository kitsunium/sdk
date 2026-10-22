package raft

import (
	"fmt"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// proposeDenyPin proposes a DenyPin of target at leader r.
func proposeDenyPin(r *Raft, target core.NodeID) error {
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:deny", Change: wire.ChangeDenyPin, Target: target})
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}}); code != 0 {
		return fmt.Errorf("refused: %v", code)
	}
	return nil
}

// ackedLeader is leader 1 of members 1, 2 and 3 at term 2 whose no-op voter
// 2 acknowledged, which commits it; voter 3 never answered.
func ackedLeader(mut mutation) *Raft {
	r := leaderOf(2, 0, nil, mut, false)
	ack := msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = r.log.lastIndex()
	r.Step(r.now, ack)
	r.Ready()
	return r
}

// admittedKey returns the key the admission set holds for id.
func admittedKey(r *Raft, id core.NodeID) [32]byte {
	for _, m := range r.admitted() {
		if m.ID == id {
			return m.SPKI
		}
	}
	return [32]byte{}
}

// checkDenyPinKeepsAQuorum: a DenyPin is the strict reconfiguration check's
// like any change that cuts a member: refused for this leader, and refused
// while the voters that answer, its target left out, are not a majority.
// Denying the one live voter of three cut the last quorum (#530).
func checkDenyPinKeepsAQuorum(mut mutation) error {
	r := ackedLeader(mut)
	if err := proposeDenyPin(r, 2); err == nil {
		return fmt.Errorf("the pin of the one answering voter was denied while voter 3 is silent")
	}
	if err := proposeDenyPin(r, 1); err == nil {
		return fmt.Errorf("the leader's own pin was denied")
	}
	hb := msg(core.MsgHeartbeatResp, 3, 1, 2)
	r.Step(r.now, hb)
	if err := proposeDenyPin(r, 2); err != nil {
		return fmt.Errorf("a DenyPin with a majority left was %v", err)
	}
	return nil
}

// checkDeniedPinAdmittedUntilCommit: the denied member stays admitted, with
// the pin the committed configuration names, until the DenyPin commits; the
// acknowledgement the entry needs may be its own (#530, D4).
func checkDeniedPinAdmittedUntilCommit(mut mutation) error {
	r := ackedLeader(mut)
	r.Step(r.now, msg(core.MsgHeartbeatResp, 3, 1, 2))
	if err := proposeDenyPin(r, 2); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if k := admittedKey(r, 2); k != [32]byte{2} {
		return fmt.Errorf("the denied pin left the admission set at append: %x", k[:2])
	}
	st := r.log.lastIndex()
	r.StorageDone(r.Ready().Seq, st, 2)
	ack := msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = st
	r.Step(r.now, ack)
	r.Ready()
	if r.log.committed != st {
		return fmt.Errorf("setup: the DenyPin did not commit (%d < %d)", r.log.committed, st)
	}
	if k := admittedKey(r, 2); k != [32]byte{} {
		return fmt.Errorf("the denied pin is still admitted once its entry committed: %x", k[:2])
	}
	return nil
}

func TestADenyPinKeepsAQuorum(t *testing.T) {
	if err := checkDenyPinKeepsAQuorum(0); err != nil {
		t.Fatal(err)
	}
}

func TestADeniedPinIsAdmittedUntilItCommits(t *testing.T) {
	if err := checkDeniedPinAdmittedUntilCommit(0); err != nil {
		t.Fatal(err)
	}
}
