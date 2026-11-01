package raft

import (
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #761: a voter sends its PreVote, the question #542's record answers, to
// the members of its own configuration at the addresses it lists them at.
// #603's guard counted a voter that stays as a stale voter's voucher at
// whatever address it moved to, and a move was never judged: a voter down
// while the leader was admitted kept a configuration whose last member that
// stayed UpdateAddress then moved, and came back dialling a dead address,
// refused by the leader's key, for as long as that leader led.

// proposeMove proposes, at leader r, the move of target to addr.
func proposeMove(r *Raft, target core.NodeID, addr string) errs.Code {
	data := wire.AppendAdmin(nil, &wire.AdminValue{
		Operator: "op:move", Change: wire.ChangeUpdateAddress, Target: target,
		Membership: core.MembershipValue{Members: []core.MemberValue{{ID: target, Address: addr}}},
	})
	_, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}})
	return code
}

// The exact branch: a voter that stays vouches only at the address the
// stale voter's configuration names; the leader vouches at any, since it
// dials the stale voter.
func TestVouchedNeedsTheVoterAtTheAddressTheStaleVoterKnows(t *testing.T) {
	at := func(m core.MembershipValue, addrs ...string) core.MembershipValue {
		for i := range m.Members {
			m.Members[i].Address = addrs[i]
		}
		return m
	}
	known := at(testConf(1, 2, 3), "a1", "a2", "a3")
	next := at(testConf(1, 2, 4), "a1", "a2-moved", "a4")
	if vouched(&next, &known, 1, 4, 0) {
		t.Error("a voter knowing 2 at a2 and 3, which left, was taken as reachable once 2 moved to a2-moved")
	}
	next = at(testConf(1, 2, 4), "a1", "a2", "a4")
	if !vouched(&next, &known, 1, 4, 0) {
		t.Error("a voter knowing 2 at the address 2 still serves was taken as stranded")
	}
	known = at(testConf(1, 2, 4), "a1", "a2", "a4")
	next = at(testConf(1, 2, 4), "a1", "a2-moved", "a4-moved")
	if !vouched(&next, &known, 1, 4, 0) {
		t.Error("a voter listing the leader at its old address was taken as stranded: the leader dials it")
	}
}

// Over unlistedHistory, node 3 is down since before leader 4 was admitted:
// it knows nodes 1 and 2 at the addresses the founding configuration gives
// them, and a frame reaches a member only at the address it listens at.
// Node 1 moves, node 2 still vouches; node 2 is then the last member node 3
// reaches, and neither its move nor its removal is admitted while node 3 is
// down. Node 3 comes back, asks node 2 for the record of leader 4 (#542)
// and catches up; node 2 then moves, and node 3 follows it there.
func TestTheMoveOfTheLastMemberAStaleVoterReachesIsRefused(t *testing.T) {
	n := unlistedCluster(t, unlistedCase{})
	n.down[3] = true
	listens := map[core.NodeID]string{}
	n.reach = func(m core.MessageValue) bool {
		if dialled(n.nodes[m.From], m.To) == listens[m.To] {
			return true
		}
		n.nodes[m.From].ReportUnreachable(m.To)
		return false
	}
	r3, r4 := n.nodes[3], n.nodes[4]
	settle := func() { n.tick(2 * testTiming().ElectionTimeoutMax) }
	settle()
	if code := proposeMove(r4, 1, "a1-moved"); code != 0 {
		t.Fatalf("moving node 1 while node 3 still reaches node 2 was refused: %v", code)
	}
	listens[1] = "a1-moved"
	settle()
	if code := proposeMove(r4, 2, "a2-moved"); code != codeMembershipInvalid {
		t.Fatalf("moving node 2, the last member node 3 (down) reaches at an address it knows, was %v, want MEMBERSHIP_INVALID: node 3 would dial node 1 at %q and node 2 at %q, and refuse leader 4", code, dialled(r3, 1), dialled(r3, 2))
	}
	if err := proposeRemove(r4, 2); err == nil {
		t.Fatal("removing node 2, the last member node 3 (down) reaches since node 1 moved, was admitted")
	}

	n.down[3] = false
	n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
	if r3.log.lastIndex() != r4.log.lastIndex() || r3.log.committed != r4.log.committed {
		t.Fatalf("node 3 never caught up through node 2:%v", n)
	}
	if code := proposeMove(r4, 2, "a2-moved"); code != 0 {
		t.Fatalf("node 3 is back and heard on a configuration that lists leader 4, yet moving node 2 was refused: %v", code)
	}
	listens[2] = "a2-moved"
	settle()
	if m, _ := r3.conf().Member(2); m.Address != "a2-moved" || r3.log.committed != r4.log.committed {
		t.Fatalf("node 3 names node 2 at %q at commit %d, want a2-moved at %d:%v", m.Address, r3.log.committed, r4.log.committed, n)
	}
}

// The move is judged as the removal is: only an answering member that may
// vouch. Over a compacted log where voter 2 never answered (its
// configuration unknown, stranded already), moving voter 3, which answers,
// is refused; moving the silent voter 2 itself, the leader, or a learner
// that never voted is admitted, as is moving 3 once 2 answers at the last
// index.
func TestAMoveIsJudgedOnlyForAnAnsweringMemberThatMayVouch(t *testing.T) {
	if code := proposeMove(leaderOverCompacted(0), 3, "a3-moved"); code != codeMembershipInvalid {
		t.Errorf("moving answering voter 3 while voter 2, whose configuration is unknown, may know no one else was %v, want MEMBERSHIP_INVALID", code)
	}
	if code := proposeMove(leaderOverCompacted(0), 2, "a2-moved"); code != 0 {
		t.Errorf("moving the silent voter 2 itself was refused: %v; a member that does not answer vouches for no one now", code)
	}
	if code := proposeMove(leaderOverCompacted(0), 1, "a1-moved"); code != 0 {
		t.Errorf("moving the leader was refused: %v; it dials every voter that lists it", code)
	}
	r := leaderOverCompacted(0)
	ackFrom(r, 2, r.log.lastIndex())
	if code := proposeMove(r, 3, "a3-moved"); code != 0 {
		t.Errorf("voter 2 answered at the last index and knows the leader, yet moving 3 was refused: %v", code)
	}

	conf := testConf(2, 3, 1, 4, 5, 6)
	conf.Members[5].Voter = false
	r = nodeOf(1, 6, conf, Protocol2, 0)
	r.term, r.vote = 6, 1
	r.becomeLeader()
	r.StorageDone(r.Ready().Seq, r.log.lastIndex(), 6)
	for _, id := range []core.NodeID{3, 4, 5, 6} {
		ackFrom(r, id, r.log.lastIndex())
	}
	if !r.strands(r.conf()) {
		t.Fatal("setup: voter 2 is not stranded already")
	}
	if code := proposeMove(r, 6, "a6-moved"); code != 0 {
		t.Errorf("moving an answering learner, which vouches for no voter, was refused: %v", code)
	}
}
