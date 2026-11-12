package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A follower of protocol 4 takes the leader's last index from a heartbeat
// only beside FlagProtocol4, and keeps it for that leader and term: a
// heartbeat of a leader of protocol 3, which leaves RejectHint 0 and sends
// no FlagProtocol4, tells it nothing, and a node of protocol 3 ignores the
// field whatever the flags (#919).
func TestAFollowerHearsTheLeadersLastIndexOnlyBesideFlagProtocol4(t *testing.T) {
	cases := []struct {
		name   string
		speaks uint32
		flags  core.MessageFlags
		want   uint64
	}{
		{name: "a leader of protocol 4", flags: announcementOf(Protocol4), want: 50},
		{name: "a leader of protocol 3", flags: announcementOf(Protocol3), want: 0},
		{name: "a follower of protocol 3", speaks: Protocol3, flags: announcementOf(Protocol4), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startOn(2, tc.speaks, snapAt(Protocol3))
			f.Step(t0, core.MessageValue{
				Version: 1, Group: 1, Kind: core.MsgHeartbeat, From: 1, To: 2, Term: 1, Commit: 10,
				RejectHint: 50, Flags: tc.flags,
			})
			st := f.Status()
			if st.Lead != 1 {
				t.Fatalf("setup: the follower leads to %d, want 1", st.Lead)
			}
			if st.LeaderLast != tc.want {
				t.Fatalf("LeaderLast %d, want %d", st.LeaderLast, tc.want)
			}
			if !st.Appended.IsZero() {
				t.Fatalf("a heartbeat set Appended %v: only an append with entries does", st.Appended)
			}
		})
	}
}

// The mixed-cluster case (#919): node 3 runs protocol 3 beside two nodes
// of protocol 4. Every frame of node 3 lacks FlagProtocol4 and every
// heartbeat it sends leaves RejectHint 0; it ignores the leader's last
// index the others send, stops on nothing, and keeps the raise to 4
// refused. A follower of protocol 4 under a leader of protocol 4 hears the
// leader's last index and when it last took entries. Upgraded, node 3
// announces protocol 4 and the raise goes through.
func TestAMixedClusterOfProtocol3And4KeepsReplicatingAndRefusesTheRaise(t *testing.T) {
	s := newSim(simOptions{seed: 919, speaks: map[core.NodeID]uint32{3: Protocol3}})
	s.onSend = func(m core.MessageValue) {
		old := s.opt.speaks[m.From] == Protocol3
		if old && m.Flags&core.FlagProtocol4 != 0 {
			s.fail("node %d of protocol 3 sent %v with FlagProtocol4", m.From, m.Kind)
		}
		if m.Kind == core.MsgHeartbeat && old && m.RejectHint != 0 {
			s.fail("node %d of protocol 3 sent a heartbeat with RejectHint %d", m.From, m.RejectHint)
		}
		if m.Kind == core.MsgHeartbeat && !old && m.Flags&core.FlagProtocol4 == 0 {
			s.fail("node %d of protocol 4 sent a heartbeat without FlagProtocol4", m.From)
		}
	}
	s.run(s.after(5*time.Second), true)
	if code := s.raiseTo(Protocol3); code != 0 {
		t.Fatalf("the raise to 3 was refused in a cluster that speaks it: %v", code)
	}
	if code := s.raiseTo(Protocol4); code != codeVersion {
		t.Fatalf("the raise to 4 with node 3 at protocol 3: %v, want VERSION_UNSUPPORTED", code)
	}
	s.run(s.after(3*time.Second), true)
	if s.err != nil {
		t.Fatal(s.err)
	}
	l := s.leader()
	if l == nil {
		t.Fatal("no leader after 8 s")
	}
	last := l.r.Status().Last
	heard := 0
	for _, id := range s.ids {
		n := s.nodes[id]
		if n == l {
			continue
		}
		if n.unsupported.Index != 0 {
			t.Fatalf("node %d stopped on entry %d: the heartbeat's last index is no format it reads", id, n.unsupported.Index)
		}
		st := n.r.Status()
		if s.opt.speaks[id] == Protocol3 || s.opt.speaks[l.id] == Protocol3 {
			if st.LeaderLast != 0 {
				t.Fatalf("node %d under leader %d heard LeaderLast %d across protocol 3", id, l.id, st.LeaderLast)
			}
			continue
		}
		heard++
		if st.LeaderLast == 0 || st.LeaderLast > last {
			t.Fatalf("node %d heard LeaderLast %d of leader %d at %d", id, st.LeaderLast, l.id, last)
		}
		if st.Appended.IsZero() {
			t.Fatalf("node %d under load took no append with entries", id)
		}
	}
	if s.opt.speaks[l.id] != Protocol3 && heard != 1 {
		t.Fatalf("%d followers of protocol 4 heard the leader, want 1", heard)
	}
	if st := l.r.Status(); st.LeaderLast != 0 || !st.Appended.IsZero() {
		t.Fatalf("the leader reports LeaderLast %d and Appended %v, want neither", st.LeaderLast, st.Appended)
	}

	s.crash(3)
	delete(s.opt.speaks, 3)
	s.push(&event{at: s.after(100 * time.Millisecond), kind: evRestart, node: 3})
	s.run(s.after(3*time.Second), true)
	if code := s.raiseTo(Protocol4); code != 0 {
		t.Fatalf("the raise to 4 once every member speaks it: %v", code)
	}
	if s.err != nil {
		t.Fatal(s.err)
	}
	for _, id := range s.ids {
		if got := s.nodes[id].r.Status().Cluster; got != Protocol4 {
			t.Fatalf("node %d runs at cluster version %d, want 4", id, got)
		}
	}
}
