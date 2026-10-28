package raft

import (
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #724: the TCP transport is one-way per connection. A node hears its
// leader on the connection the leader dials, and answers on its own
// outbound link, dialled to the address its own configuration names. A
// member (node 3) down while UpdateAddress moved another member (node 1)
// came back with node 1's old address; once node 1 led, node 3 heard it,
// so it neither campaigned nor asked, and every answer it sent went to the
// old address. Node 1's probe was never answered, node 1 never sent the
// entry carrying its new address, and node 3 stayed out of the quorum for
// as long as node 1 led: one more failure left no leader.

// movedAt is where each member listens: node 1 moved from a1 to a1-moved.
var movedAt = map[core.NodeID]string{1: "a1-moved", 2: "a2", 3: "a3"}

// movedConf is members 1, 2 and 3, node 3 a learner when learner is set,
// each at a<id>, node 1 at addr1.
func movedConf(addr1 string, learner bool) core.MembershipValue {
	m := testConf(1, 2, 3)
	for i := range m.Members {
		m.Members[i].Address = fmt.Sprintf("a%d", m.Members[i].ID)
	}
	m.Members[0].Address = addr1
	m.Members[2].Voter = !learner
	return m
}

// movedHistory is the log nodes 1 and 2 hold, committed through 4 at term
// 1: two commands, the UpdateAddress that moved node 1 to a1-moved, a
// command. Node 3 holds the first two entries only.
func movedHistory(learner bool) []core.EntryValue {
	ents := chainEntries(0, 1, 1, 1)
	upd := engineEntry(ents[1].ChainSum, 3, core.EntryMembership, &wire.AdminValue{
		Operator: "op:move", Change: wire.ChangeUpdateAddress, Target: 1, Membership: movedConf("a1-moved", learner),
	})
	return append(append(ents, upd), chainEntries(upd.ChainSum, 4, 1)...)
}

// movedMember starts node id over ents, committed through commit, from the
// founding configuration that names node 1 at a1, speaking protocol speaks
// (0: ProtocolMax) at the cluster version cv.
func movedMember(id core.NodeID, ents []core.EntryValue, commit uint64, learner bool, cv, speaks uint32) *Raft {
	cfg := &Config{
		ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, speaks: speaks,
	}
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 1, Commit: commit},
		Snapshot:  core.SnapshotMetaValue{Membership: movedConf("a1", learner), ClusterVersion: cv, ClusterGeneration: uint64(cv)},
		Entries:   slices.Clone(ents), Now: t0,
	})
	r.Ready()
	return r
}

// dialled is the address r's admission set names for id, the one its
// transport dials.
func dialled(r *Raft, id core.NodeID) string {
	for _, m := range r.admitted() {
		if m.ID == id {
			return m.Address
		}
	}
	return ""
}

// movedCase is how node 3 and its peers start.
type movedCase struct {
	// learner makes node 3 a learner.
	learner bool
	// peers is the protocol nodes 1 and 2 speak, 0 for ProtocolMax; the
	// cluster version is 2, or 1 when they speak 1.
	peers uint32
}

// movedCluster is nodes 1, 2 and 3 as c says, node 3 behind the move, on a
// network where a frame reaches its receiver only when the sender dials the
// address the receiver listens at (movedAt), the sender's transport
// reporting the receiver unreachable otherwise. Node 1 leads at term 2.
func movedCluster(t *testing.T, c movedCase) *unlistedNet {
	t.Helper()
	h := movedHistory(c.learner)
	cv := Protocol2
	if c.peers != 0 {
		cv = min(cv, c.peers)
	}
	n := newUnlistedNet(t, movedMember(1, h, 4, c.learner, cv, c.peers), movedMember(2, h, 4, c.learner, cv, c.peers),
		movedMember(3, h[:2], 2, c.learner, cv, 0))
	n.reach = func(m core.MessageValue) bool {
		if dialled(n.nodes[m.From], m.To) == movedAt[m.To] {
			return true
		}
		n.nodes[m.From].ReportUnreachable(m.To)
		return false
	}
	n.elect(1)
	if got := dialled(n.nodes[3], 1); got != "a1" {
		t.Fatalf("setup: node 3 dials node 1 at %q, want a1", got)
	}
	return n
}

// TestTheMemberBehindAMovedLeaderDialsItAtTheCommittedAddress: node 3, a
// voter or a learner, hears leader 1 and cannot reach it. It asks node 2
// for the record of the leader it follows, dials node 1 at the address
// committed there, is fed the entry that moved node 1 and catches up; the
// record ends with its configuration naming that address. Its questions
// moved no term. Once node 2 fails, nodes 1 and 3, two voters of three,
// still have a leader.
func TestTheMemberBehindAMovedLeaderDialsItAtTheCommittedAddress(t *testing.T) {
	for _, learner := range []bool{false, true} {
		t.Run(fmt.Sprintf("learner=%v", learner), func(t *testing.T) {
			n := movedCluster(t, movedCase{learner: learner})
			r1, r3 := n.nodes[1], n.nodes[3]
			term := r1.term
			n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
			t.Logf("under leader 1:%v", n)
			if r3.log.lastIndex() != r1.log.lastIndex() || r3.log.committed != r1.log.committed {
				t.Fatalf("node 3 never caught up with leader 1 (last %d < %d): its answers went to node 1's old address %s", r3.log.lastIndex(), r1.log.lastIndex(), dialled(r3, 1))
			}
			if m, _ := r3.conf().Member(1); m.Address != "a1-moved" || dialled(r3, 1) != "a1-moved" {
				t.Fatalf("node 3's configuration names node 1 at %q and dials it at %q, want a1-moved", m.Address, dialled(r3, 1))
			}
			if n.leader() != 1 || r1.term != term || r3.term != term {
				t.Fatalf("node 3's questions moved the leadership: leader %d, terms %d and %d (was %d)", n.leader(), r1.term, r3.term, term)
			}
			if learner {
				return
			}
			if r1.FailureTolerance(n.now) != 1 {
				t.Fatalf("leader 1 does not hear node 3: failure tolerance %d", r1.FailureTolerance(n.now))
			}
			n.down[2] = true
			n.tick(10 * testTiming().ElectionTimeoutMax)
			t.Logf("after node 2 failed:%v", n)
			if l := n.leader(); l == 0 || !n.nodes[l].QuorumActive(n.now) {
				t.Fatal("no leader with nodes 1 and 3 up, two voters of three")
			}
		})
	}
}

// TestAnUnansweredMemberBehindAMovedLeaderMovesNothing: nodes 1 and 2
// speak protocol 1 and send no record, as a release before #542 does.
// Node 3 asks at most once every askElectionTimeouts, stays behind as it
// did before it asked, and leader 1 keeps leading at its term.
func TestAnUnansweredMemberBehindAMovedLeaderMovesNothing(t *testing.T) {
	n := movedCluster(t, movedCase{peers: 1})
	r1, r3 := n.nodes[1], n.nodes[3]
	term := r1.term
	n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
	t.Logf("under leader 1:%v", n)
	if dialled(r3, 1) != "a1" || r3.log.lastIndex() != 2 {
		t.Fatalf("node 3 dials node 1 at %q, last %d, with no record sent", dialled(r3, 1), r3.log.lastIndex())
	}
	if n.leader() != 1 || r1.term != term || r3.term != term || r3.role != core.RoleFollower {
		t.Fatalf("node 3's questions moved the leadership: leader %d, terms %d and %d (was %d), node 3 %v", n.leader(), r1.term, r3.term, term, r3.role)
	}
}

// addrRecord is a MsgLeaderRecord from from to node 3 at term, read at the
// commit index, for rec.
func addrRecord(from core.NodeID, term, index uint64, rec core.MemberValue) core.MessageValue {
	data := wire.AppendMembership(nil, &core.MembershipValue{Members: []core.MemberValue{rec}})
	m := msg(core.MsgLeaderRecord, from, 3, term)
	m.Index = index
	m.Entries = []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}}
	return m
}

// TestTheLeaderAddressIsTakenOnlyForTheUnreachedLeaderUnderItsKey: node 3
// follows leader 1 at term 2. It asks nobody while it reaches node 1, then,
// once its transport reports node 1 unreachable, asks node 2 alone, once in
// askElectionTimeouts, keeping its role and its term. It dials node 1 at a
// record's address only for that leader, under the key its configuration
// pins, from a member of its configuration at its term, read at a commit
// past its own; the record ends once its commit reaches that one.
func TestTheLeaderAddressIsTakenOnlyForTheUnreachedLeaderUnderItsKey(t *testing.T) {
	h := movedHistory(false)
	r := movedMember(3, h[:2], 2, false, Protocol2, 0)
	now := t0.Add(10 * time.Second)
	hb := msg(core.MsgHeartbeat, 1, 3, 2)
	hb.Commit = 2
	r.Step(now, hb)
	r.Ready()
	moved := core.MemberValue{ID: 1, SPKI: [32]byte{1}, Address: "a1-moved", Voter: true}
	take := func(m core.MessageValue) ReadyValue {
		r.Step(now, m)
		return r.Ready()
	}
	if rd := take(addrRecord(2, 2, 5, moved)); dialled(r, 1) != "a1" || rd.LeaderAddress.ID != 0 {
		t.Fatalf("a record taken while node 1 was reached: node 3 dials %q", dialled(r, 1))
	}
	r.Tick(now)
	if q := asked(r.Ready()); len(q) != 0 {
		t.Fatalf("node 3 asked %d members while it reaches its leader", len(q))
	}
	r.ReportUnreachable(1)
	r.Tick(now)
	q := asked(r.Ready())
	if _, ok := q[2]; len(q) != 1 || !ok || q[2].Term != 3 || q[2].Flags&core.FlagProtocol2 == 0 {
		t.Fatalf("node 3 that cannot reach leader 1 asked %v, want node 2 alone at term 3 announcing protocol 2", q)
	}
	if r.role != core.RoleFollower || r.term != 2 || r.lead != 1 {
		t.Fatalf("node 3 that asked is %v at term %d following %d, want a follower of 1 at 2", r.role, r.term, r.lead)
	}
	r.Tick(now.Add(time.Second))
	if q := asked(r.Ready()); len(q) != 0 {
		t.Fatalf("node 3 asked again within %v", askElectionTimeouts*testTiming().ElectionTimeoutMax)
	}
	otherKey := moved
	otherKey.SPKI = [32]byte{9}
	notLeader := core.MemberValue{ID: 2, SPKI: [32]byte{2}, Address: "a2-moved", Voter: true}
	noAddress := moved
	noAddress.Address = ""
	for _, x := range []struct {
		name string
		m    core.MessageValue
	}{
		{"under another key", addrRecord(2, 2, 5, otherKey)},
		{"for a member that does not lead", addrRecord(2, 2, 5, notLeader)},
		{"from a non-member", addrRecord(9, 2, 5, moved)},
		{"read at a commit this node holds", addrRecord(2, 2, 2, moved)},
		{"at an older term", addrRecord(2, 1, 5, moved)},
		{"with no address", addrRecord(2, 2, 5, noAddress)},
	} {
		if rd := take(x.m); dialled(r, 1) != "a1" || dialled(r, 2) != "a2" || rd.LeaderAddress.ID != 0 {
			t.Fatalf("a record %s was taken: node 3 dials node 1 at %q, node 2 at %q", x.name, dialled(r, 1), dialled(r, 2))
		}
	}
	rd := take(addrRecord(2, 2, 5, moved))
	if dialled(r, 1) != "a1-moved" || rd.LeaderAddress.Address != "a1-moved" || !slices.ContainsFunc(rd.Membership, func(m core.MemberValue) bool {
		return m.ID == 1 && m.Address == "a1-moved" && m.SPKI == moved.SPKI
	}) {
		t.Fatalf("the leader's record was not taken: node 3 dials node 1 at %q, Ready %+v", dialled(r, 1), rd.Membership)
	}
	// Leader 1, now heard, rewrites node 3 through the move and commits
	// past the record's index: the configuration names the address, and the
	// record ends.
	app := msg(core.MsgApp, 1, 3, 2)
	app.Index, app.LogTerm, app.Commit = 2, 1, 5
	app.Entries = append(slices.Clone(h[2:]), chainEntries(h[3].ChainSum, 5, 2)...)
	r.Step(now, app)
	r.Ready()
	if r.leadAddr.ID != 0 || dialled(r, 1) != "a1-moved" || r.log.committed != 5 {
		t.Fatalf("the record outlived the configuration that names its address: record %+v, dials %q, commit %d", r.leadAddr, dialled(r, 1), r.log.committed)
	}
}
