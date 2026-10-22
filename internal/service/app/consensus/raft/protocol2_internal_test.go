package raft

import (
	"fmt"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// proposeRaise proposes raising the cluster version to v at leader r.
func proposeRaise(r *Raft, v uint32) errs.Code {
	data := wire.AppendAdmin(nil, &wire.AdminValue{
		Operator: "op:raise", Pre: core.PreconditionSpec{Log: r.cfg.Log, Generation: r.clusterGen}, ClusterVersion: v,
	})
	_, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryClusterVersion, BodySum: wire.BodySum(data), Data: data}})
	return code
}

// heartbeatFrom is a heartbeat answer of the leader's term from id, with or
// without the protocol 2 announcement.
func heartbeatFrom(r *Raft, id core.NodeID, announces bool) core.MessageValue {
	m := msg(core.MsgHeartbeatResp, id, 1, r.term)
	if announces {
		m.Flags |= core.FlagProtocol2
	}
	return m
}

// checkRaiseAnnounced: the cluster version is raised to 2 only once every
// member of the configuration was heard in this term announcing it, and a
// member that comes back on the previous release withdraws its announcement
// at its next message. A member of the previous release stops on the raise
// and on every format written after it (D18, lot S3).
func checkRaiseAnnounced(mut mutation) error {
	r := ackedLeader(mut)
	if code := proposeRaise(r, Protocol2); code == 0 {
		return fmt.Errorf("raised while no member announced protocol 2")
	}
	r.Step(r.now, heartbeatFrom(r, 2, true))
	r.Step(r.now, heartbeatFrom(r, 3, true))
	r.Step(r.now, heartbeatFrom(r, 3, false))
	if code := proposeRaise(r, Protocol2); code == 0 {
		return fmt.Errorf("raised while member 3's last message did not announce protocol 2")
	}
	r.Step(r.now, heartbeatFrom(r, 3, true))
	if code := proposeRaise(r, Protocol2); code != 0 {
		return fmt.Errorf("setup: a raise every member announced was refused: %v", code)
	}
	return nil
}

func TestARaiseWaitsForEveryMemberToAnnounceIt(t *testing.T) {
	if err := checkRaiseAnnounced(0); err != nil {
		t.Fatal(err)
	}
	if err := checkRaiseAnnounced(mutRaiseUnannounced); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// A node announces protocol 2 on every message it sends; a node of the
// previous release announces nothing.
func TestEveryMessageAnnouncesTheProtocol(t *testing.T) {
	r := ackedLeader(0)
	r.Step(r.now, heartbeatFrom(r, 2, true))
	rd := r.Ready()
	if len(rd.Messages) == 0 {
		r.Tick(r.now.Add(r.timing.HeartbeatInterval))
		rd = r.Ready()
	}
	sent := append(rd.Messages, rd.AfterSync...)
	if len(sent) == 0 {
		t.Fatal("setup: the leader sent nothing")
	}
	for _, m := range sent {
		if m.Flags&core.FlagProtocol2 == 0 {
			t.Fatalf("message %v sent without the protocol 2 announcement", m.Kind)
		}
	}
	old := newTest(2, testState{term: 1}, 0, false)
	old.cfg.speaks = 1
	old.Step(t0, msg(core.MsgHeartbeat, 1, 2, 1))
	ord := old.Ready()
	answers := append(ord.Messages, ord.AfterSync...)
	if len(answers) == 0 {
		t.Fatal("setup: the node of protocol 1 answered nothing")
	}
	for _, m := range answers {
		if m.Flags&core.FlagProtocol2 != 0 {
			t.Fatalf("a node of protocol 1 announced protocol 2 on %v", m.Kind)
		}
	}
}

// A node of the previous release that finds the raise to 2 committed stops
// there, its cluster version unchanged: the rollback after the raise is
// refused cleanly, never run on formats it cannot read.
func TestANodeOfThePreviousReleaseStopsOnTheRaise(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 0, ents: chainEntries(0, 1, 1)}, 0, false)
	f.cfg.speaks = 1
	data := wire.AppendAdmin(nil, &wire.AdminValue{ClusterVersion: Protocol2})
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 2}, Kind: core.EntryClusterVersion, Data: data, BodySum: wire.BodySum(data)}
	e.ChainSum = wire.ChainSum(f.log.chain(1), &e)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Index, m.LogTerm, m.Commit, m.Entries = 1, 1, 2, []core.EntryValue{e}
	f.Step(t0, m)
	if u := f.Ready().Unsupported; u.Index != 2 || u.Kind != core.EntryClusterVersion {
		t.Fatalf("a node of protocol 1 applied the raise to 2: %+v", u)
	}
	if f.clusterVer != 0 {
		t.Fatalf("cluster version raised to %d on a node of protocol 1", f.clusterVer)
	}
	g := newTest(3, testState{term: 1, commit: 0, ents: chainEntries(0, 1, 1)}, 0, false)
	g.Step(t0, m)
	if u := g.Ready().Unsupported; u.Index != 0 || g.Status().Cluster != Protocol2 {
		t.Fatalf("a node of protocol 2 did not apply the raise: %+v, cluster %d", u, g.Status().Cluster)
	}
}
