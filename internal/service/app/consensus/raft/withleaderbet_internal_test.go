package raft

import (
	"slices"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #765, option (b): #603's guard keeps its two bets on the leader, and
// Isolated names a voter whose reach rests on one, beside the voters it
// finds stranded.

// The first bet: the leader is counted at any address. Founders 1, 2 and 3;
// 4 joined and was promoted while 3 was down, so 3 knows {1, 2, 3}; 1 leads.
// 2's removal is admitted, 1 vouching for 3 where 3 knows it; then 1 moves
// itself, which the guard never judges: 3 is reached only through 1, at an
// address it does not know. Once 4 leads, 3 is stranded.
func TestAVoterReachedOnlyThroughAMovedLeaderIsNamed(t *testing.T) {
	h := unlistedHistory()
	commit := h[len(h)-1].Position.Index
	n := newUnlistedNet(t, unlistedMember(1, h, commit, 0, Protocol2, 0), unlistedMember(2, h, commit, 0, Protocol2, 0),
		unlistedMember(3, h[:2], 0, 0, Protocol2, 0), unlistedMember(4, h, commit, 0, Protocol2, 0))
	n.down[3] = true
	n.elect(1)
	r1, r4 := n.nodes[1], n.nodes[4]
	settle := func() { n.tick(2 * testTiming().ElectionTimeoutMax) }
	settle()
	//: 1 vouches for 3 where 3 knows it.
	if err := proposeRemove(r1, 2); err != nil {
		t.Fatalf("setup: Remove(2) = %v", err)
	}
	settle()
	//: nothing to say yet.
	if got := r1.Isolated(); len(got) != 0 {
		t.Fatalf("setup: Isolated = %v before 1 moved", got)
	}
	//: the leader's own move is admitted.
	if code := proposeMove(r1, 1, "a1-moved"); code != 0 {
		t.Fatalf("UpdateAddress(1) on leader 1 = %v, want it admitted: the leader's own move is never judged", code)
	}
	settle()
	moved := []IsolationValue{{ID: 3, Flags: IsolationLeaderMoved}}
	//: 1 must name 3, reached only through it at an address 3 does not know.
	if got := r1.Isolated(); !slices.Equal(got, moved) {
		t.Errorf("Isolated = %v, want %v", got, moved)
	}
	// 1 hands over to 4, which 3 does not know.
	//: the transfer starts.
	if code := r1.TransferLeadership(n.now, 4); code != 0 {
		t.Fatalf("setup: TransferLeadership(4) = %v", code)
	}
	settle()
	//: 4 must lead.
	if n.leader() != 4 {
		t.Fatalf("setup: 4 does not lead:%v", n)
	}
	stranded := []IsolationValue{{ID: 3, Flags: IsolationStranded}}
	//: under 4, 3 is stranded.
	if got := r4.Isolated(); !slices.Equal(got, stranded) {
		t.Errorf("Isolated on 4 = %v, want %v", got, stranded)
	}
}

// The second bet: over a compacted log, a voter the snapshot's base lists
// after the leader is counted as knowing it as a voter. Leader 1 of voters
// 2, 3, 1, 4 and 5 over a log compacted at index 10: 5, which never
// answered, is listed after 1, and reached on that bet alone; the removal
// of 3, which answers, stays admitted. 2, listed before 1, is stranded.
func TestAVoterReachedOnlyOnTheCompactedLogsBetIsNamed(t *testing.T) {
	leader := func(answer ...core.NodeID) *Raft {
		r := nodeOf(1, 5, testConf(2, 3, 1, 4, 5), Protocol2, 0)
		r.term, r.vote = 6, 1
		r.becomeLeader()
		r.StorageDone(r.Ready().Seq, r.log.lastIndex(), 6)
		//: the voters that answer.
		for _, id := range answer {
			ackFrom(r, id, r.log.lastIndex())
		}
		r.Ready()
		return r
	}
	r := leader(2, 3, 4)
	bet := []IsolationValue{{ID: 5, Flags: IsolationLeaderUnconfirmed}}
	//: 5 is reached on the bet alone.
	if got := r.Isolated(); !slices.Equal(got, bet) {
		t.Errorf("Isolated = %v, want %v", got, bet)
	}
	//: the removal admitted on the bet stays admitted.
	if err := proposeRemove(r, 3); err != nil || r.GuardRefusal() != nil {
		t.Errorf("Remove(3) = %v, GuardRefusal = %v: want it admitted on the bet", err, r.GuardRefusal())
	}
	stranded := []IsolationValue{{ID: 2, Flags: IsolationStranded}}
	//: 2, listed before 1, is stranded, and no bet is named for it.
	if got := leader(3, 4, 5).Isolated(); !slices.Equal(got, stranded) {
		t.Errorf("Isolated = %v, want %v", got, stranded)
	}
}
