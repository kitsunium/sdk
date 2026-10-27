package raft

import (
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #718: a voter TruncateAndStaySilent left silent never campaigns (#524),
// so it never sent the PreVote #542's leader record answers, and #639's
// question was a learner's only. A silent voter whose truncated log ends
// before the admission of the current leader refused that leader at every
// handshake, its admission set being its own configuration, heard nobody
// else, and so was never rewritten, never ended its silence and never voted
// again for as long as that leader led: one voter lost with no signal, a
// raise to protocol 3 refused, and one more failure left no leader. A voter
// with no table, which never campaigns either (D7), had the same gap.

// unlistedNet is a synchronous network of step functions under the TCP
// transport's admission: a frame reaches its receiver only when the
// receiver's admission set holds its sender (SetMembership(admitted())),
// and every write is durable at once.
type unlistedNet struct {
	t     *testing.T
	nodes map[core.NodeID]*Raft
	ids   []core.NodeID
	down  map[core.NodeID]bool
	queue []core.MessageValue
	now   time.Time
	// installs are the nodes a leader asked the driver to send a snapshot.
	installs map[core.NodeID]bool
}

func newUnlistedNet(t *testing.T, nodes ...*Raft) *unlistedNet {
	n := &unlistedNet{t: t, nodes: map[core.NodeID]*Raft{}, down: map[core.NodeID]bool{}, installs: map[core.NodeID]bool{}}
	for _, r := range nodes {
		n.nodes[r.id] = r
		n.ids = append(n.ids, r.id)
	}
	n.now = t0.Add(10 * time.Second)
	return n
}

func (n *unlistedNet) admits(to, from core.NodeID) bool {
	return slices.ContainsFunc(n.nodes[to].admitted(), func(m core.MemberValue) bool { return m.ID == from })
}

// bindsNothing fails the test when a silent node sent what the silence
// forbids, or campaigns: a vote request, a granted vote or PreVote, or a
// positive acknowledgement (#524, R3-09). Its question is a PreVote.
func (n *unlistedNet) bindsNothing(r *Raft, sent []core.MessageValue) {
	n.t.Helper()
	if r.cfg.SilentIndex == 0 {
		return
	}
	if r.role == core.RolePreCandidate || r.role == core.RoleCandidate || r.role == core.RoleLeader {
		n.t.Fatalf("silent node %d is %v at term %d", r.id, r.role, r.term)
	}
	for _, m := range sent {
		switch {
		case m.Kind == core.MsgVote,
			(m.Kind == core.MsgVoteResp || m.Kind == core.MsgPreVoteResp) && !m.Reject,
			m.Kind == core.MsgAppResp && !m.Reject:
			n.t.Fatalf("silent node %d sent %v to %d (reject %v)", r.id, m.Kind, m.To, m.Reject)
		}
	}
}

// drain runs every live node's Ready, makes its writes durable at once, and
// delivers every frame the receiver admits until nothing moves.
func (n *unlistedNet) drain() {
	n.t.Helper()
	for range 100000 {
		moved := false
		for _, id := range n.ids {
			r := n.nodes[id]
			if n.down[id] || !r.HasReady() {
				continue
			}
			moved = true
			rd := r.Ready()
			n.bindsNothing(r, append(slices.Clone(rd.Messages), rd.AfterSync...))
			for _, to := range rd.SendSnapshot {
				n.installs[to] = true
			}
			n.queue = append(n.queue, rd.Messages...)
			if rd.NeedsWrite() {
				var idx, term uint64
				if k := len(rd.Entries); k > 0 {
					idx, term = rd.Entries[k-1].Position.Index, rd.Entries[k-1].Position.Term
				}
				r.StorageDone(rd.Seq, idx, term)
			}
			n.queue = append(n.queue, rd.AfterSync...)
		}
		for len(n.queue) > 0 {
			m := n.queue[0]
			n.queue = n.queue[1:]
			moved = true
			if n.down[m.To] || n.down[m.From] || !n.admits(m.To, m.From) {
				continue
			}
			n.nodes[m.To].Step(n.now, m)
		}
		if !moved {
			return
		}
	}
	n.t.Fatal("the network never settled")
}

// tick advances every live node by d, in steps of 10 ms.
func (n *unlistedNet) tick(d time.Duration) {
	n.t.Helper()
	for end := n.now.Add(d); n.now.Before(end); {
		n.now = n.now.Add(10 * time.Millisecond)
		for _, id := range n.ids {
			if !n.down[id] {
				n.nodes[id].Tick(n.now)
			}
		}
		n.drain()
	}
}

// leader is the live node that leads, 0 for none.
func (n *unlistedNet) leader() core.NodeID {
	for _, id := range n.ids {
		if !n.down[id] && n.nodes[id].role == core.RoleLeader {
			return id
		}
	}
	return 0
}

// elect ticks node id alone until it leads.
func (n *unlistedNet) elect(id core.NodeID) {
	n.t.Helper()
	r := n.nodes[id]
	for i := 0; i < 1000 && r.role != core.RoleLeader; i++ {
		n.now = n.now.Add(10 * time.Millisecond)
		r.Tick(n.now)
		n.drain()
	}
	if r.role != core.RoleLeader {
		n.t.Fatalf("setup: node %d did not win: %v", id, r.role)
	}
}

func (n *unlistedNet) String() string {
	s := ""
	for _, id := range n.ids {
		r := n.nodes[id]
		if n.down[id] {
			s += fmt.Sprintf(" n%d=down", id)
			continue
		}
		s += fmt.Sprintf(" n%d={%v term %d lead %d last %d commit %d silent %d}", id, r.role, r.term, r.lead, r.log.lastIndex(), r.log.committed, r.cfg.SilentIndex)
	}
	return s
}

// unlistedHistory is the log every member but 3 holds, committed through 5
// at term 1 over founders 1, 2 and 3: two commands, AddLearner(4),
// Promote(4), a command. Node 3 holds the first two entries only: it was
// down while 4 was admitted, and its log was cut at index 3.
func unlistedHistory() []core.EntryValue {
	ents := chainEntries(0, 1, 1, 1)
	learner := testConf(1, 2, 3, 4)
	learner.Members[3].Voter = false
	add := engineEntry(ents[1].ChainSum, 3, core.EntryMembership, &wire.AdminValue{
		Operator: "op:add", Change: wire.ChangeAddLearner, Target: 4, Membership: learner,
	})
	promote := engineEntry(add.ChainSum, 4, core.EntryMembership, &wire.AdminValue{
		Operator: "op:promote", Change: wire.ChangePromote, Target: 4, Membership: testConf(1, 2, 3, 4),
	})
	return append(append(ents, add, promote), chainEntries(promote.ChainSum, 5, 1)...)
}

// unlistedMember starts node id over ents, committed through commit, at
// cluster version cv, speaking protocol speaks (0: ProtocolMax), silent at
// (1, silentIndex) unless silentIndex is 0.
func unlistedMember(id core.NodeID, ents []core.EntryValue, commit, silentIndex uint64, cv, speaks uint32) *Raft {
	cfg := &Config{
		ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, speaks: speaks,
	}
	if silentIndex != 0 {
		cfg.SilentTerm, cfg.SilentIndex = 1, silentIndex
	}
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 1, Commit: commit},
		Snapshot:  core.SnapshotMetaValue{Membership: testConf(1, 2, 3), ClusterVersion: cv, ClusterGeneration: uint64(cv)},
		Entries:   slices.Clone(ents), Now: t0,
	})
	r.Ready()
	return r
}

// unlistedCase is how node 3 and its peers start.
type unlistedCase struct {
	// silent leaves node 3 silent past its cut, noTable with no table.
	silent, noTable bool
	// peers is the protocol members 1 and 2 speak, 0 for ProtocolMax; the
	// cluster version is 2, or 1 when they speak 1.
	peers uint32
}

// unlistedCluster is members 1 to 4 with node 3 cut back to index 2, as c
// says. Node 4 leads at term 2.
func unlistedCluster(t *testing.T, c unlistedCase) *unlistedNet {
	t.Helper()
	h := unlistedHistory()
	var si uint64
	if c.silent {
		si = 3
	}
	cv := Protocol2
	if c.peers != 0 {
		cv = min(cv, c.peers)
	}
	stale := unlistedMember(3, h[:2], 0, si, cv, 0)
	stale.cfg.NoTable = c.noTable
	n := newUnlistedNet(t, unlistedMember(1, h, 5, 0, cv, c.peers), unlistedMember(2, h, 5, 0, cv, c.peers),
		stale, unlistedMember(4, h, 5, 0, cv, 0))
	n.elect(4)
	if n.admits(3, 4) {
		t.Fatal("setup: node 3 admits leader 4 before any record")
	}
	return n
}

// TestASilentVoterRejoinsALeaderItsLogDoesNotList: leader 4 was admitted
// past the point node 3's log was cut at. The silent node asks its members
// for the record of the leader they follow, as a learner does, admits 4,
// is rewritten, ends its silence and catches up; it never campaigned nor
// voted while silent (bindsNothing). Leader 4 hears it, keeps a failure
// tolerance of one, and once node 1 fails 2, 3 and 4 still have a leader.
func TestASilentVoterRejoinsALeaderItsLogDoesNotList(t *testing.T) {
	n := unlistedCluster(t, unlistedCase{silent: true})
	n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
	r3, r4 := n.nodes[3], n.nodes[4]
	t.Logf("under leader 4:%v", n)
	if r3.cfg.SilentIndex != 0 || r3.log.lastIndex() != r4.log.lastIndex() {
		t.Fatalf("node 3 is still silent at %d or behind leader 4 (%d < %d): it never asked for the record of a leader its configuration does not list", r3.cfg.SilentIndex, r3.log.lastIndex(), r4.log.lastIndex())
	}
	if pr := r4.prs[3]; pr == nil || pr.facts&peerHeard == 0 || r4.FailureTolerance(n.now) != 1 {
		t.Fatalf("leader 4 does not hear node 3: failure tolerance %d", r4.FailureTolerance(n.now))
	}
	n.down[1] = true
	n.tick(10 * testTiming().ElectionTimeoutMax)
	t.Logf("after node 1 failed:%v", n)
	if n.leader() == 0 {
		t.Fatal("no leader with nodes 2, 3 and 4 up, 3 of the 4 voters")
	}
}

// TestAVoterWithNoTableAsksALeaderItsLogDoesNotList: the same node 3, not
// silent but with no table, never campaigns either (D7); it asks for the
// record, admits leader 4, follows it and is sent the install it waits for.
func TestAVoterWithNoTableAsksALeaderItsLogDoesNotList(t *testing.T) {
	n := unlistedCluster(t, unlistedCase{noTable: true})
	r3 := n.nodes[3]
	n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
	t.Logf("under leader 4:%v", n)
	if !n.admits(3, 4) || r3.lead != 4 || !n.installs[3] {
		t.Fatalf("node 3, a voter with no table, admits leader 4 %v, follows %d, install sent %v", n.admits(3, 4), r3.lead, n.installs[3])
	}
}

// TestAnUnansweredSilentVoterStaysSilent: members 1 and 2 speak protocol 1
// and send no record, as a release before #542 does. The silent node asks
// again every askElectionTimeouts, binds nothing, and stays silent as it did
// before it asked; leader 4 keeps leading at its term.
func TestAnUnansweredSilentVoterStaysSilent(t *testing.T) {
	n := unlistedCluster(t, unlistedCase{silent: true, peers: 1})
	r3, r4 := n.nodes[3], n.nodes[4]
	term := r4.term
	n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
	t.Logf("under leader 4:%v", n)
	if r3.cfg.SilentIndex == 0 || n.admits(3, 4) || r3.log.lastIndex() != 2 {
		t.Fatalf("node 3 left its silence (%d) or admitted 4 (%v) with no record sent", r3.cfg.SilentIndex, n.admits(3, 4))
	}
	if n.leader() != 4 || r4.term != term {
		t.Fatalf("the silent node's questions moved the leadership: leader %d, term %d (was %d)", n.leader(), r4.term, term)
	}
}

// noTableVoter is node 2 of members 1, 2, 3, a voter with no table (D7)
// over the log [1, 2], which heard no leader since its start at t0.
func noTableVoter(mut mutation) *Raft {
	r := NewRaft(&Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, NoTable: true,
	}, &StartState{
		HardState: core.HardState{Term: 2, Vote: 1}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: chainEntries(0, 1, 1, 2), Now: t0,
	})
	r.mut = mut
	r.Ready()
	return r
}

// checkStrandedVoterAsks: a voter that may not campaign, silent (#524) or
// with no table (D7), asks every other member of its configuration for the
// record of the leader they follow once it heard no leader for
// askElectionTimeouts maximum election timeouts, once in that time, and
// tells the driver, as a learner does (#639). The question keeps it a
// follower at its term. It used to wait for nothing: it never sent the
// PreVote #542's record answers, and stayed cut off from a leader its log
// does not list for as long as that leader led (#718).
func checkStrandedVoterAsks(mut mutation) error {
	wait := askElectionTimeouts * testTiming().ElectionTimeoutMax
	for _, x := range []struct {
		name string
		r    *Raft
	}{{"a silent voter", silentVoter(mut)}, {"a voter with no table", noTableVoter(mut)}} {
		r := x.r
		r.Tick(t0.Add(wait - time.Millisecond))
		if q := asked(r.Ready()); len(q) != 0 {
			return fmt.Errorf("%s asked before %v without a leader: %d questions", x.name, wait, len(q))
		}
		r.Tick(t0.Add(wait))
		rd := r.Ready()
		q := asked(rd)
		pv, ok := q[3]
		if _, one := q[1]; len(q) != 2 || !ok || !one || !rd.LeaderUnheard {
			return fmt.Errorf("%s that heard no leader for %v asked %d members (LeaderUnheard %v), want 1 and 3 and set", x.name, wait, len(q), rd.LeaderUnheard)
		}
		if pv.Flags&core.FlagProtocol2 == 0 || pv.Term != 3 || pv.LogTerm != 2 || pv.Index != 2 {
			return fmt.Errorf("%s's question to 3: %+v", x.name, pv)
		}
		if r.role != core.RoleFollower || r.term != 2 {
			return fmt.Errorf("%s that asked is %v at term %d, want a follower at 2", x.name, r.role, r.term)
		}
		r.Tick(t0.Add(wait + time.Second))
		if q := asked(r.Ready()); len(q) != 0 {
			return fmt.Errorf("%s asked again within %v", x.name, wait)
		}
	}
	return nil
}
