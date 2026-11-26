package raft

import (
	"errors"
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The mixed-cluster case of protocol 5 (#1472): node 3 runs the previous
// release, protocol 4, beside two nodes of protocol 5, under load. No frame
// of node 3 carries FlagProtocol5 but a read asking for a round, whose bit
// it is (FlagNoLease); every frame of a node of protocol 5 but a read
// carries it; nobody stops; the raise to 5 is refused while node 3 speaks
// 4. Upgraded, node 3 announces protocol 5 and the raise goes through,
// every node then at cluster version 5, which changes no format.
func TestAMixedClusterOfProtocol4And5KeepsReplicatingAndRefusesTheRaise(t *testing.T) {
	s := newSim(simOptions{seed: 1472, speaks: map[core.NodeID]uint32{3: Protocol4}})
	s.onSend = func(m core.MessageValue) {
		p5 := m.Flags&core.FlagProtocol5 != 0
		switch old := s.opt.speaks[m.From] == Protocol4; {
		case m.Kind == core.MsgReadIndex:
			// The bit is FlagNoLease on a read, whatever the release.
		case old && p5:
			s.fail("node %d of protocol 4 sent %v with FlagProtocol5", m.From, m.Kind)
		case !old && !p5:
			s.fail("node %d of protocol 5 sent %v without FlagProtocol5", m.From, m.Kind)
		}
	}
	s.run(s.after(5*time.Second), true)
	if code := s.raiseTo(Protocol4); code != 0 {
		t.Fatalf("the raise to 4 was refused in a cluster that speaks it: %v", code)
	}
	if code := s.raiseTo(Protocol5); code != codeVersion {
		t.Fatalf("the raise to 5 with node 3 at protocol 4: %v, want VERSION_UNSUPPORTED", code)
	}
	s.run(s.after(3*time.Second), true)
	if s.err != nil {
		t.Fatal(s.err)
	}
	for _, id := range s.ids {
		if n := s.nodes[id]; n.unsupported.Index != 0 {
			t.Fatalf("node %d stopped on entry %d: protocol 5 is no format a node of 4 reads", id, n.unsupported.Index)
		}
		if got := s.nodes[id].r.Status().Cluster; got != Protocol4 {
			t.Fatalf("node %d runs at cluster version %d, want 4", id, got)
		}
	}

	s.crash(3)
	delete(s.opt.speaks, 3)
	s.push(&event{at: s.after(100 * time.Millisecond), kind: evRestart, node: 3})
	s.run(s.after(3*time.Second), true)
	if code := s.raiseTo(Protocol5); code != 0 {
		t.Fatalf("the raise to 5 once every member speaks it: %v", code)
	}
	if s.err != nil {
		t.Fatal(s.err)
	}
	for _, id := range s.ids {
		if got := s.nodes[id].r.Status().Cluster; got != Protocol5 {
			t.Fatalf("node %d runs at cluster version %d, want 5", id, got)
		}
	}
}

// TestAReadCarriesFlagNoLeaseOnlyWhenItAsksForARound: FlagProtocol5 is
// FlagNoLease's bit, so a follower of protocol 5 sets it on every frame but
// a read, which carries it only when it asks for a round: on every read, a
// leader of any release would confirm by a round each read it could serve
// under its lease (#1472).
func TestAReadCarriesFlagNoLeaseOnlyWhenItAsksForARound(t *testing.T) {
	type tc struct {
		name    string
		noLease bool
	}
	runCase := func(t *testing.T, c tc) {
		f := newTest(2, testState{term: 2}, 0, false)
		hb := msg(core.MsgHeartbeat, 1, 2, 2)
		hb.Flags = announcementOf(Protocol5)
		f.Step(t0, hb)
		var read, answer core.MessageValue
		for _, m := range answersOf(f.Ready()) {
			if m.Kind == core.MsgHeartbeatResp {
				answer = m
			}
		}
		f.ReadIndex(t0, 7, c.noLease)
		for _, m := range answersOf(f.Ready()) {
			if m.Kind == core.MsgReadIndex {
				read = m
			}
		}
		switch {
		case answer.Flags&announcements[Protocol5] != announcements[Protocol5]:
			t.Fatalf("setup: the heartbeat's answer announces %v, want protocol 5", answer.Flags)
		case read.Kind != core.MsgReadIndex:
			t.Fatal("setup: the follower sent no read to its leader")
		case (read.Flags&core.FlagNoLease != 0) != c.noLease:
			t.Fatalf("a read asking for a round %v carries FlagNoLease %v (flags %v)", c.noLease, read.Flags&core.FlagNoLease != 0, read.Flags)
		}
	}
	for _, c := range []tc{
		{name: "under the lease"},
		{name: "confirmed by a round", noLease: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAReadNeverAnnounces: a read carries FlagNoLease, FlagProtocol5's bit,
// only when it asks for a round, so a leader reads a member's announcement
// off every frame but a read (O3 of the safety review of #1472's second
// version). A member of protocol 4 whose last frame is a read asking for a
// round keeps the raise to 5 refused: read as its announcement, that read
// let the raise through, and the member stopped on it once committed
// (D18). A member of protocol 5 whose last frame is a read under the lease
// keeps its announcement of 5.
func TestAReadNeverAnnounces(t *testing.T) {
	type tc struct {
		name    string
		from    core.NodeID
		speaks  uint32
		noLease bool
	}
	runCase := func(t *testing.T, c tc) {
		l := leaderOf(2, 0, nil, 0, false)
		for _, a := range []struct {
			from core.NodeID
			v    uint32
		}{{2, Protocol5}, {3, Protocol4}} {
			hr := msg(core.MsgHeartbeatResp, a.from, 1, 2)
			hr.Flags = announcementOf(a.v)
			l.Step(l.now, hr)
		}
		if code := l.checkAnnounced(Protocol5); code != codeVersion {
			t.Fatalf("setup: the raise to 5 with node 3 announcing 4: %v, want VERSION_UNSUPPORTED", code)
		}
		read := msg(core.MsgReadIndex, c.from, 1, 2)
		read.Flags, read.Context = announcementOf(c.speaks)&^core.FlagProtocol5, 5
		if c.noLease {
			read.Flags |= core.FlagNoLease
		}
		l.Step(l.now, read)
		switch {
		case c.speaks < Protocol5 && l.checkAnnounced(Protocol5) != codeVersion:
			t.Fatalf("node %d of protocol %d, its last frame a read (flags %08b): the raise to 5 was admitted", c.from, c.speaks, read.Flags)
		case c.speaks >= Protocol5 && l.prs[c.from].facts&peerProtocol5 == 0:
			t.Fatalf("node %d of protocol 5, its last frame a read (flags %08b): its announcement of 5 was withdrawn", c.from, read.Flags)
		}
	}
	for _, c := range []tc{
		{name: "a member of protocol 4, a read asking for a round", from: 3, speaks: Protocol4, noLease: true},
		{name: "a member of protocol 4, a read under the lease", from: 3, speaks: Protocol4},
		{name: "a member of protocol 5, a read under the lease", from: 2, speaks: Protocol5},
		{name: "a member of protocol 5, a read asking for a round", from: 2, speaks: Protocol5, noLease: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestARunAsAPreviousReleaseSpeaksProtocol4: a node run as a release before
// #1472 (termFromAnswers) speaks protocol 4 at most, as such a release does
// (O5 of the safety review of #1472's second version, where it spoke 5): it
// announces no protocol 5, refuses to raise the cluster version to 5, and
// stops on a raise to 5 it finds committed, its cluster version unchanged.
// A node of this release announces 5 and applies the raise.
func TestARunAsAPreviousReleaseSpeaksProtocol4(t *testing.T) {
	type tc struct {
		name   string
		before bool
	}
	runCase := func(t *testing.T, c tc) {
		l := leaderOf(2, 0, nil, 0, false)
		l.cfg.termFromAnswers = c.before
		raise := wire.AdminValue{ClusterVersion: Protocol5, Pre: core.PreconditionSpec{Log: l.cfg.Log, Generation: l.clusterGen}}
		f := newTest(2, testState{term: 1, ents: chainEntries(0, 1, 1)}, 0, false)
		f.cfg.termFromAnswers = c.before
		data := wire.AppendAdmin(nil, &wire.AdminValue{ClusterVersion: Protocol5})
		e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 2}, Kind: core.EntryClusterVersion, Data: data, BodySum: wire.BodySum(data)}
		e.ChainSum = wire.ChainSum(f.log.chain(1), &e)
		m := msg(core.MsgApp, 1, 2, 1)
		m.Index, m.LogTerm, m.Commit, m.Entries = 1, 1, 2, []core.EntryValue{e}
		f.Step(t0, m)
		u := f.Ready().Unsupported
		switch announces5, refused := l.announces()&peerProtocol5 != 0, l.checkClusterVersion(&raise) == codeVersion && l.speaks() < Protocol5; {
		case c.before && (announces5 || !refused):
			t.Fatalf("a leader run as a previous release announces 5 %v, refuses the raise to 5 for its own protocol %v", announces5, refused)
		case c.before && (u.Index != 2 || f.Status().Cluster != 0):
			t.Fatalf("a follower run as a previous release applied the raise to 5: %+v, cluster %d", u, f.Status().Cluster)
		case !c.before && (!announces5 || refused):
			t.Fatalf("a leader of this release announces 5 %v, refuses the raise to 5 for its own protocol %v", announces5, refused)
		case !c.before && (u.Index != 0 || f.Status().Cluster != Protocol5):
			t.Fatalf("a follower of this release did not apply the raise to 5: %+v, cluster %d", u, f.Status().Cluster)
		}
	}
	for _, c := range []tc{
		{name: "this release"},
		{name: "run as a release before #1472", before: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// upgradeGate runs s under load until the gate of a rolling upgrade's step
// opens on three voters, at most limit (R7-45): a leader every voter
// acknowledges, following it at its term and holding its commit, so that
// one may fail with a majority left. It reports whether it opened.
func (s *sim) upgradeGate(limit time.Duration) bool {
	for end := s.after(limit); s.now.Before(end) && s.err == nil; s.run(s.after(100*time.Millisecond), true) {
		l := s.leader()
		if l == nil {
			continue
		}
		ls, all := l.r.Status(), true
		for _, id := range s.ids {
			n := s.nodes[id]
			if st := n.r.Status(); !n.alive || (id != l.id && (st.Lead != l.id || st.Term != ls.Term || st.Last < ls.Commit)) {
				all = false
			}
		}
		if all {
			return true
		}
	}
	return false
}

// upgrade stops node id, upgrades it to this release and starts it again
// 100 ms later.
func (s *sim) upgrade(id core.NodeID) {
	s.crash(id)
	delete(s.opt.speaks, id)
	s.push(&event{at: s.after(100 * time.Millisecond), kind: evRestart, node: id})
	s.run(s.after(200*time.Millisecond), true)
}

// oldLeader returns the leader once it runs the previous release, handing
// the leadership from a node of this release to one of the previous one
// first, at most 10 s; nil when none led.
func (s *sim) oldLeader() *simNode {
	for end := s.after(10 * time.Second); s.now.Before(end) && s.err == nil; s.run(s.after(500*time.Millisecond), true) {
		l := s.leader()
		if l == nil {
			continue
		}
		if s.opt.speaks[l.id] != 0 {
			return l
		}
		for _, id := range s.ids {
			if s.opt.speaks[id] != 0 {
				l.r.TransferLeadership(s.clock(l), id)
				s.process(l)
				break
			}
		}
	}
	return nil
}

// rollingUpgradeTo5 rolls three nodes of protocol 4 over to this release
// under load, one at a time, each step waiting for the gate (R7-45), as
// D20's rollout script does. After the first node is upgraded, the leader,
// of protocol 4, hands its leadership to it, and its vote requests are
// lost: it is a candidate one term ahead of the leader, which takes its
// leadership back (O1 of the safety review of #1472's second version).
// Every node then upgraded, the raise to 5 goes through and no node stops.
// It returns the first step whose gate stayed shut for 30 s, or what went
// wrong.
func rollingUpgradeTo5(seed uint64) error {
	s := newSim(simOptions{seed: seed, speaks: map[core.NodeID]uint32{1: Protocol4, 2: Protocol4, 3: Protocol4}})
	s.run(s.after(5*time.Second), true)
	if code := s.raiseTo(Protocol4); code != 0 {
		return fmt.Errorf("setup: the raise to 4: %v", code)
	}
	if !s.upgradeGate(30 * time.Second) {
		return fmt.Errorf("setup: the gate never opened: %v", s.err)
	}
	l := s.leader()
	first := s.ids[0]
	if first == l.id {
		first = s.ids[1]
	}
	s.upgrade(first)
	if !s.upgradeGate(30 * time.Second) {
		return fmt.Errorf("step 1, node %d upgraded: the gate stayed shut: %v", first, s.err)
	}
	if l = s.oldLeader(); l == nil {
		return errors.New("no node of protocol 4 led")
	}
	led := l.r.Status().Term
	if code := l.r.TransferLeadership(s.clock(l), first); code != 0 {
		return fmt.Errorf("the transfer to node %d was refused: %v", first, code)
	}
	s.process(l)
	for _, id := range s.ids {
		s.cut[[2]core.NodeID{first, id}] = true
	}
	x := s.nodes[first]
	for end := s.after(time.Second); s.now.Before(end) && !(x.r.Status().Role == core.RoleCandidate && x.r.Status().Term == led+1); {
		s.run(s.after(20*time.Millisecond), true)
	}
	s.heal()
	if st := x.r.Status(); st.Term != led+1 {
		return fmt.Errorf("setup: node %d at term %d after the transfer of term %d", first, st.Term, led)
	}
	if !s.upgradeGate(30 * time.Second) {
		st := x.r.Status()
		return fmt.Errorf("step 2, the transfer to node %d failed: the gate stayed shut, node %d %v at term %d following %d: %v",
			first, first, st.Role, st.Term, st.Lead, s.err)
	}
	for _, id := range s.ids {
		if s.opt.speaks[id] == 0 || (s.leader() != nil && s.leader().id == id) {
			continue
		}
		s.upgrade(id)
		if !s.upgradeGate(30 * time.Second) {
			return fmt.Errorf("step 3, node %d upgraded: the gate stayed shut: %v", id, s.err)
		}
		break
	}
	if code := s.raiseTo(Protocol5); code != codeVersion {
		return fmt.Errorf("the raise to 5 with one node of protocol 4 left: %v, want VERSION_UNSUPPORTED", code)
	}
	for _, id := range s.ids {
		if s.opt.speaks[id] == 0 {
			continue
		}
		if l := s.leader(); l != nil && l.id == id {
			to := s.ids[0]
			if to == id {
				to = s.ids[1]
			}
			l.r.TransferLeadership(s.clock(l), to)
			s.process(l)
			s.run(s.after(2*time.Second), true)
		}
		s.upgrade(id)
		if !s.upgradeGate(30 * time.Second) {
			return fmt.Errorf("step 4, node %d upgraded: the gate stayed shut: %v", id, s.err)
		}
	}
	if code := s.raiseTo(Protocol5); code != 0 {
		return fmt.Errorf("the raise to 5 once every node is upgraded: %v", code)
	}
	if s.err != nil {
		return s.err
	}
	for _, id := range s.ids {
		n := s.nodes[id]
		if n.unsupported.Index != 0 || n.r.Status().Cluster != Protocol5 {
			return fmt.Errorf("node %d stopped on entry %d, at cluster version %d", id, n.unsupported.Index, n.r.Status().Cluster)
		}
	}
	return nil
}

// TestARollingUpgradeTo5ReopensEveryGate rolls a cluster of three nodes of
// protocol 4 over to this release, 16 seeds, the leadership handed to the
// first node upgraded with its vote requests lost (rollingUpgradeTo5): at
// every step the gate reopens, and the raise to 5 goes through at the end.
// A leader of a previous release, sent nothing by the node upgraded one
// term ahead, kept its leadership on the third node's acknowledgements,
// and the gate, a failure tolerance of at least one, stayed shut (O1 of
// the safety review of #1472's second version).
func TestARollingUpgradeTo5ReopensEveryGate(t *testing.T) {
	type tc struct {
		name string
		seed uint64
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		if err := rollingUpgradeTo5(c.seed); err != nil {
			t.Fatal(err)
		}
	}
	for seed := uint64(1); seed <= 16; seed++ {
		c := tc{name: fmt.Sprintf("seed %d", seed), seed: seed}
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
