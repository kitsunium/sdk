package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// admin proposes an engine entry of kind with payload a to whichever node
// leads, again while no node leads or a membership change is pending, for
// up to 10 s of simulated time, then lets it commit everywhere. It returns
// the leader's answer.
func (s *sim) admin(kind core.EntryKind, a wire.AdminValue) errs.Code {
	code := codeNotLeader
	for end := s.after(10 * time.Second); s.now.Before(end) && s.err == nil; s.run(s.after(200*time.Millisecond), false) {
		l := s.leader()
		if l == nil {
			continue
		}
		if kind == core.EntryClusterVersion {
			a.Pre = core.PreconditionSpec{Log: l.r.cfg.Log, Generation: l.r.clusterGen}
		}
		data := wire.AppendAdmin(nil, &a)
		_, code = l.r.Propose(s.clock(l), []core.EntryValue{{Kind: kind, BodySum: wire.BodySum(data), Data: data}})
		s.process(l)
		if code != codeNotLeader && code != codeChangePending {
			break
		}
	}
	s.run(s.after(time.Second), false)
	return code
}

// raiseTo is the raise of the cluster version to v.
func (s *sim) raiseTo(v uint32) errs.Code {
	return s.admin(core.EntryClusterVersion, wire.AdminValue{Operator: "op:raise", ClusterVersion: v})
}

// addLearner is the AddLearner of id over key.
func (s *sim) addLearner(id core.NodeID, key [32]byte) errs.Code {
	m := core.MemberValue{ID: id, Slot: "y", SPKI: key, Address: "a"}
	return s.admin(core.EntryMembership, wire.AdminValue{
		Operator: "op:add", Change: wire.ChangeAddLearner, Target: id,
		Membership: core.MembershipValue{Members: []core.MemberValue{m}},
	})
}

// remove is the Remove of id.
func (s *sim) remove(id core.NodeID) errs.Code {
	return s.admin(core.EntryMembership, wire.AdminValue{Operator: "op:remove", Change: wire.ChangeRemove, Target: id})
}

// checkProtocolOf fails t unless every node runs at cluster version v,
// stopped on nothing, with the retired keys want, and every membership
// entry it holds of a payload version at most maxVersion.
func checkProtocolOf(t *testing.T, s *sim, v, maxVersion uint32, want [][32]byte) {
	t.Helper()
	if s.err != nil {
		t.Fatal(s.err)
	}
	for _, id := range s.ids {
		n := s.nodes[id]
		if n.unsupported.Index != 0 {
			t.Fatalf("node %d stopped on entry %d (%v): it met a format it cannot read", id, n.unsupported.Index, n.unsupported.Kind)
		}
		if got := n.r.Status().Cluster; got != v {
			t.Fatalf("node %d runs at cluster version %d, want %d", id, got, v)
		}
		conf := n.r.conf()
		if len(conf.RetiredKeys) != len(want) {
			t.Fatalf("node %d retired keys %v, want %v", id, conf.RetiredKeys, want)
		}
		for _, k := range want {
			if !conf.IsRetiredKey(k) {
				t.Fatalf("node %d does not hold key %x retired", id, k[:2])
			}
		}
		if n.base.ClusterVersion < Protocol3 && len(n.base.Membership.RetiredKeys) != 0 {
			t.Fatalf("node %d cut a snapshot of cluster version %d carrying retired keys", id, n.base.ClusterVersion)
		}
		for i := range n.ents {
			if e := &n.ents[i]; e.Kind == core.EntryMembership && e.Version > maxVersion {
				t.Fatalf("node %d holds a membership entry of payload version %d at %d, want at most %d", id, e.Version, e.Position.Index, maxVersion)
			}
		}
	}
}

// A mixed cluster: node 3 runs the previous release, protocol 2, beside
// two nodes of protocol 3. It keeps the raise to 3 refused, and until the
// raise nothing of protocol 3 is written: a removal retires no key, every
// membership entry is of payload version 2 at most, and the node of
// protocol 2 reads every entry and stops on none. Upgraded and restarted,
// it announces protocol 3, the raise goes through, and from then on the
// key of a member removed is never admitted again (D18, #648). A key
// removed before the raise was never kept: protocol 3 cannot know it.
func TestAMixedClusterRaisesTo3OnceEveryMemberSpeaksIt(t *testing.T) {
	s := newSim(simOptions{seed: 648, snapEvery: 16, speaks: map[core.NodeID]uint32{3: Protocol2}})
	s.run(s.after(5*time.Second), true)
	if code := s.raiseTo(Protocol2); code != 0 {
		t.Fatalf("the raise to 2 was refused in a cluster that speaks it: %v", code)
	}
	if code := s.raiseTo(Protocol3); code != codeVersion {
		t.Fatalf("the raise to 3 with node 3 at protocol 2: %v, want VERSION_UNSUPPORTED", code)
	}
	old, fresh := [32]byte{0x99}, [32]byte{0x9a}
	if code := s.addLearner(4, old); code != 0 {
		t.Fatalf("setup: add learner 4: %v", code)
	}
	if code := s.remove(4); code != 0 {
		t.Fatalf("setup: remove 4: %v", code)
	}
	s.run(s.after(2*time.Second), true)
	checkProtocolOf(t, s, Protocol2, wire.AdminVersion2, nil)
	if code := s.addLearner(5, old); code != 0 {
		t.Fatalf("below protocol 3 the key of removed member 4 was refused, as it never was: %v", code)
	}
	// A learner that never answers would keep the raise refused.
	if code := s.remove(5); code != 0 {
		t.Fatalf("setup: remove 5: %v", code)
	}

	s.crash(3)
	delete(s.opt.speaks, 3)
	s.push(&event{at: s.after(100 * time.Millisecond), kind: evRestart, node: 3})
	s.run(s.after(3*time.Second), true)
	if code := s.raiseTo(Protocol3); code != 0 {
		t.Fatalf("the raise to 3 was refused once every member speaks it: %v", code)
	}
	if code := s.addLearner(6, fresh); code != 0 {
		t.Fatalf("setup: add learner 6: %v", code)
	}
	if code := s.remove(6); code != 0 {
		t.Fatalf("setup: remove 6: %v", code)
	}
	s.run(s.after(2*time.Second), true)
	checkProtocolOf(t, s, Protocol3, wire.AdminVersion3, [][32]byte{fresh})
	if code := s.addLearner(7, fresh); code != codeMembershipInvalid {
		t.Fatalf("at protocol 3 a learner over the key of removed member 6: %v, want MEMBERSHIP_INVALID", code)
	}
	if code := s.addLearner(7, [32]byte{0x9b}); code != 0 {
		t.Fatalf("at protocol 3 a fresh key was refused: %v", code)
	}
	if s.err != nil {
		t.Fatal(s.err)
	}
}
