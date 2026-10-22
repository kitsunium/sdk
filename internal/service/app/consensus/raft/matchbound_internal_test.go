package raft

import (
	"fmt"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// checkMatchBoundedByLastIndex: an acknowledgement past the leader's last
// index comes from no honest follower (Leader Completeness). Taken as match,
// that of a follower whose snapshot metadata rotted, with the third voter
// silent, committed a new write held by the leader's disk alone (#546).
func checkMatchBoundedByLastIndex(mut mutation) error {
	r := leaderOf(2, 0, nil, mut, false)
	r.Ready()
	ack := msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = r.log.lastIndex() + 1<<30
	r.Step(r.now, ack)
	r.Ready()
	if pr := r.prs[2]; pr != nil && pr.match > r.log.lastIndex() {
		return fmt.Errorf("match %d past the leader's last index %d", pr.match, r.log.lastIndex())
	}
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryCommand}}); code != 0 {
		return fmt.Errorf("setup: propose refused: %v", code)
	}
	r.Ready()
	if r.log.committed >= r.log.lastIndex() {
		return fmt.Errorf("index %d committed with voter 2's acknowledgement past the log and voter 3 silent", r.log.committed)
	}
	return nil
}

func TestAnAcknowledgementPastTheLastIndexIsNoMatch(t *testing.T) {
	if err := checkMatchBoundedByLastIndex(0); err != nil {
		t.Fatal(err)
	}
	if err := checkMatchBoundedByLastIndex(mutMatchPastLastIndex); err == nil {
		t.Fatal("the mutation was not caught")
	}
}
