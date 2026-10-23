package raft

import (
	"fmt"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// proposeRemove proposes the removal of target at leader r.
func proposeRemove(r *Raft, target core.NodeID) error {
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:remove", Change: wire.ChangeRemove, Target: target})
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}}); code != 0 {
		return fmt.Errorf("refused: %v", code)
	}
	return nil
}

// leaderOverCompacted is leader 1 at term 6 of voters 2, 3, 1, 4 and 5 in
// that order, over a log compacted at index 10: its no-op committed, and
// voters 3, 4 and 5 answered. Voter 2 never did.
func leaderOverCompacted(mut mutation) *Raft {
	r := nodeOf(1, 5, testConf(2, 3, 1, 4, 5), Protocol2, mut)
	r.term, r.vote = 6, 1
	r.becomeLeader()
	r.StorageDone(r.Ready().Seq, r.log.lastIndex(), 6)
	for _, id := range []core.NodeID{3, 4, 5} {
		ack := msg(core.MsgAppResp, id, 1, 6)
		ack.Index = r.log.lastIndex()
		r.Step(r.now, ack)
	}
	r.Ready()
	return r
}

// checkRemoveKeepsAStaleVoterReachable: while a voter does not answer and
// the log no longer says which configuration it holds, a member that
// answers is not removed unless the voter surely knows this leader. Voter 2
// is listed before every other member, so it may know only members since
// removed; removing 3 could leave it hearing no one (#603).
func checkRemoveKeepsAStaleVoterReachable(mut mutation) error {
	r := leaderOverCompacted(mut)
	if r.log.committed < r.termStart {
		return fmt.Errorf("setup: the no-op did not commit")
	}
	if err := proposeRemove(r, 3); err == nil {
		return fmt.Errorf("an answering member was removed while voter 2, whose configuration is unknown, may know no one else")
	}
	return nil
}

func TestARemovalThatMayStrandAStaleVoterIsRefused(t *testing.T) {
	if err := checkRemoveKeepsAStaleVoterReachable(0); err != nil {
		t.Fatal(err)
	}
	if err := checkRemoveKeepsAStaleVoterReachable(mutRemoveStrands); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// A silent voter can always be removed: it vouches for no one now, and a
// dead voter must stay removable. Once the stale voter answers at an index
// the log resolves, its configuration is known, and the removal it
// survives is admitted.
func TestARemovalStrandsNoVoterOnceItsConfigurationIsKnown(t *testing.T) {
	r := leaderOverCompacted(0)
	if err := proposeRemove(r, 2); err != nil {
		t.Fatalf("the silent voter itself could not be removed: %v", err)
	}
	r = leaderOverCompacted(0)
	ack := msg(core.MsgAppResp, 2, 1, 6)
	ack.Index = r.log.lastIndex()
	r.Step(r.now, ack)
	r.Ready()
	if err := proposeRemove(r, 3); err != nil {
		t.Fatalf("voter 2 answered at the last index and still knows 1, 4 and 5, yet removing 3 was %v", err)
	}
}

// The exact branch: a voter heard at an index whose configuration lists
// no member that stays but itself is stranded by the removal.
func TestVouchedNeedsAStayingVoterOrTheLeader(t *testing.T) {
	known := testConf(1, 2, 3)
	next := testConf(1, 4, 5)
	if vouched(&next, &known, 1, 4) {
		t.Error("a voter knowing only 2 and 3, both gone, was taken as reachable by leader 4")
	}
	next = testConf(1, 3, 4)
	if !vouched(&next, &known, 1, 4) {
		t.Error("a voter knowing voter 3, which stays, was taken as stranded")
	}
	learner := testConf(1, 2, 3)
	learner.Members[2].Voter = false
	if vouched(&next, &learner, 1, 4) {
		t.Error("a voter whose only staying member was a learner to it was taken as reachable: it sends a learner no PreVote")
	}
	withLeader := testConf(1, 2, 4)
	withLeader.Members[2].Voter = false
	next = testConf(1, 4, 5)
	if !vouched(&next, &withLeader, 1, 4) {
		t.Error("a voter listing the leader, even as a learner, was taken as stranded")
	}
}
