package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// keyOf3 is the key member 3 of testConf holds, the one its removal
// retires at protocol 3.
var keyOf3 = [32]byte{3}

// keyLeader is leader 1 at term 6 of voters 1, 2 and 3 over a snapshot at
// index 10 of cluster version cv, its no-op committed by voter 2's
// acknowledgement. Voter 3 never answered.
func keyLeader(cv uint32, mut mutation) *Raft {
	r := nodeOf(1, 5, testConf(1, 2, 3), cv, mut)
	r.term, r.vote = 6, 1
	r.becomeLeader()
	commitLast(r)
	return r
}

// commitLast makes the leader's last entry durable and has voter 2
// acknowledge it, which commits it.
func commitLast(r *Raft) {
	last := r.log.lastIndex()
	r.StorageDone(r.Ready().Seq, last, r.term)
	ack := msg(core.MsgAppResp, 2, 1, r.term)
	ack.Index = last
	r.Step(r.now, ack)
	r.Ready()
}

// proposeAddLearner proposes adding learner id over key at leader r.
func proposeAddLearner(r *Raft, id core.NodeID, key [32]byte) errs.Code {
	m := core.MemberValue{ID: id, Slot: "y", SPKI: key, Address: "a"}
	data := wire.AppendAdmin(nil, &wire.AdminValue{
		Operator: "op:add", Change: wire.ChangeAddLearner, Target: id,
		Membership: core.MembershipValue{Members: []core.MemberValue{m}},
	})
	_, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}})
	return code
}

// changeAndCommit proposes a membership change with propose and commits it.
func changeAndCommit(r *Raft, what string, propose func() error) error {
	if err := propose(); err != nil {
		return fmt.Errorf("setup: %s: %w", what, err)
	}
	commitLast(r)
	if r.conf().Index > r.log.committed {
		return fmt.Errorf("setup: %s did not commit", what)
	}
	return nil
}

// checkRetiredKeyRefused: at protocol 3 the configuration keeps the key of
// a member removed, and of a pin denied, and a learner is never added over
// one, under any new id: one key, one id, for the cluster's whole life. A
// host that came back under a new id over a removed member's key, then led,
// was refused by every node whose configuration still listed the removed
// member under that key, for as long as it led (#648). A fresh key is
// admitted.
func checkRetiredKeyRefused(mut mutation) error {
	r := keyLeader(Protocol3, mut)
	if err := changeAndCommit(r, "remove 3", func() error { return proposeRemove(r, 3) }); err != nil {
		return err
	}
	if !r.conf().IsRetiredKey(keyOf3) {
		return fmt.Errorf("the key of member 3, removed at protocol 3, was not kept: %v", r.conf().RetiredKeys)
	}
	if code := proposeAddLearner(r, 4, keyOf3); code != codeMembershipInvalid {
		return fmt.Errorf("a learner was added under a new id over removed member 3's key: %v", code)
	}
	fresh := [32]byte{4}
	if err := changeAndCommit(r, "add 4 over a fresh key", func() error {
		if code := proposeAddLearner(r, 4, fresh); code != 0 {
			return fmt.Errorf("refused: %v", code)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := changeAndCommit(r, "deny 4's pin", func() error { return proposeDenyPin(r, 4) }); err != nil {
		return err
	}
	if err := changeAndCommit(r, "remove 4", func() error { return proposeRemove(r, 4) }); err != nil {
		return err
	}
	if code := proposeAddLearner(r, 5, fresh); code != codeMembershipInvalid {
		return fmt.Errorf("a learner was added over the key DenyPin withdrew from member 4: %v", code)
	}
	return nil
}

func TestARetiredKeyIsNeverAdmittedAgain(t *testing.T) {
	if err := checkRetiredKeyRefused(0); err != nil {
		t.Fatal(err)
	}
	for _, m := range []mutation{mutRetiredKeyAdmitted, mutKeyNotRetired} {
		if err := checkRetiredKeyRefused(m); err == nil {
			t.Fatalf("mutation %#x was not caught", m)
		}
	}
}

// checkNothingNewBelowProtocol3: below protocol 3 a removal keeps no key, in
// an entry of payload version 2, which a node of protocol 2 reads, and the
// key of a removed member is admitted again under a new id, as before: the
// formats of protocol 3 wait for the raise (D18, #648).
func checkNothingNewBelowProtocol3(mut mutation) error {
	r := keyLeader(Protocol2, mut)
	if err := proposeRemove(r, 3); err != nil {
		return fmt.Errorf("setup: remove 3: %w", err)
	}
	e := r.log.at(r.log.lastIndex())
	a, err := wire.ParseAdminAt(e.Version, e.Data)
	if err != nil || e.Version != wire.AdminVersion2 || len(a.Membership.RetiredKeys) != 0 {
		return fmt.Errorf("at protocol 2 the removal was written in payload version %d with retired keys %v (%v), want version %d and none",
			e.Version, a.Membership.RetiredKeys, err, wire.AdminVersion2)
	}
	commitLast(r)
	if len(r.conf().RetiredKeys) != 0 {
		return fmt.Errorf("at protocol 2 the configuration keeps retired keys %v", r.conf().RetiredKeys)
	}
	if code := proposeAddLearner(r, 4, keyOf3); code != 0 {
		return fmt.Errorf("at protocol 2 a learner over removed member 3's key was refused, as it never was before: %v", code)
	}
	return nil
}

func TestBelowProtocol3NoKeyIsRetired(t *testing.T) {
	if err := checkNothingNewBelowProtocol3(0); err != nil {
		t.Fatal(err)
	}
	if err := checkNothingNewBelowProtocol3(mutProtocol3BelowRaise); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// announcing is a heartbeat answer of the leader's term from id carrying
// the announcement of protocol version v: none below 2, both flags at 3.
func announcing(r *Raft, id core.NodeID, v uint32) core.MessageValue {
	m := msg(core.MsgHeartbeatResp, id, 1, r.term)
	if v >= Protocol2 {
		m.Flags |= core.FlagProtocol2
	}
	if v >= Protocol3 {
		m.Flags |= core.FlagProtocol3
	}
	return m
}

// checkRaise3Announced: the cluster version is raised to 3 only once every
// member of the configuration was heard in this term announcing protocol 3.
// A member of protocol 2 announces 2 only: it keeps the raise to 3 refused
// and admits the raise to 2, and a member rolled back to protocol 2
// withdraws its announcement of 3 at its next message (D18, #648).
func checkRaise3Announced(mut mutation) error {
	r := ackedLeader(mut)
	r.Step(r.now, announcing(r, 2, Protocol3))
	r.Step(r.now, announcing(r, 3, Protocol2))
	if code := proposeRaise(r, Protocol3); code == 0 {
		return fmt.Errorf("raised to 3 while member 3 announces protocol 2 only")
	}
	r.Step(r.now, announcing(r, 3, Protocol3))
	r.Step(r.now, announcing(r, 3, Protocol2))
	if code := proposeRaise(r, Protocol3); code == 0 {
		return fmt.Errorf("raised to 3 while member 3's last message announced protocol 2 only")
	}
	r.Step(r.now, announcing(r, 3, Protocol3))
	if code := proposeRaise(r, Protocol3); code != 0 {
		return fmt.Errorf("setup: a raise to 3 every member announced was refused: %v", code)
	}
	return nil
}

func TestARaiseTo3WaitsForEveryMemberToAnnounceIt(t *testing.T) {
	if err := checkRaise3Announced(0); err != nil {
		t.Fatal(err)
	}
	if err := checkRaise3Announced(mutRaiseUnannounced); err == nil {
		t.Fatal("the mutation was not caught")
	}
	r := ackedLeader(0)
	r.Step(r.now, announcing(r, 2, Protocol2))
	r.Step(r.now, announcing(r, 3, Protocol2))
	if code := proposeRaise(r, Protocol2); code != 0 {
		t.Fatalf("members of protocol 2 and 3 keep the raise to 2 refused: %v", code)
	}
}

// A node of protocol 3 announces protocol 2 and 3 on every message it
// sends; a node of protocol 2 announces 2 only.
func TestEveryMessageAnnouncesProtocol3(t *testing.T) {
	for _, tc := range []struct {
		speaks uint32
		want   core.MessageFlags
	}{{0, core.FlagProtocol2 | core.FlagProtocol3}, {Protocol2, core.FlagProtocol2}} {
		f := newTest(2, testState{term: 1}, 0, false)
		f.cfg.speaks = tc.speaks
		f.Step(t0, msg(core.MsgHeartbeat, 1, 2, 1))
		rd := f.Ready()
		answers := append(rd.Messages, rd.AfterSync...)
		if len(answers) == 0 {
			t.Fatalf("setup: the node speaking %d answered nothing", tc.speaks)
		}
		for _, m := range answers {
			if got := m.Flags & (core.FlagProtocol2 | core.FlagProtocol3); got != tc.want {
				t.Fatalf("a node speaking %d announced %#x on %v, want %#x", tc.speaks, got, m.Kind, tc.want)
			}
		}
	}
}

// A node of protocol 2 that finds the raise to 3 committed stops there, its
// cluster version unchanged: it never meets a format of protocol 3.
func TestANodeOfProtocol2StopsOnTheRaiseTo3(t *testing.T) {
	f := nodeOf(2, 1, testConf(1, 2, 3), Protocol2, 0)
	f.cfg.speaks = Protocol2
	data := wire.AppendAdminAt(nil, wire.AdminVersion2, &wire.AdminValue{
		Pre: core.PreconditionSpec{Generation: uint64(Protocol2)}, ClusterVersion: Protocol3,
	})
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 11}, Kind: core.EntryClusterVersion, Version: wire.AdminVersion2, Data: data, BodySum: wire.BodySum(data)}
	e.ChainSum = wire.ChainSum(f.log.chain(10), &e)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Index, m.LogTerm, m.Commit, m.Entries = 10, 1, 11, []core.EntryValue{e}
	f.Step(f.now, m)
	if u := f.Ready().Unsupported; u.Index != 11 || u.Kind != core.EntryClusterVersion {
		t.Fatalf("a node of protocol 2 applied the raise to 3: %+v", u)
	}
	if f.clusterVer != Protocol2 {
		t.Fatalf("cluster version %d on a node of protocol 2", f.clusterVer)
	}
	g := nodeOf(3, 1, testConf(1, 2, 3), Protocol2, 0)
	g.Step(g.now, m)
	if u := g.Ready().Unsupported; u.Index != 0 || g.Status().Cluster != Protocol3 {
		t.Fatalf("a node of protocol 3 did not apply the raise: %+v, cluster %d", u, g.Status().Cluster)
	}
}

// A snapshot of protocol 3 carries the retired keys of its configuration,
// and a node that installs it, or starts from it, refuses them once it
// leads: the keys outlive the log that retired them (#648).
func TestANodeThatInstallsASnapshotRefusesItsRetiredKeys(t *testing.T) {
	conf := testConf(1, 2)
	conf.Retired, conf.RetiredKeys = []core.NodeID{3}, [][32]byte{keyOf3}
	meta := core.SnapshotMetaValue{
		Position: core.PositionValue{Term: 1, Index: 10}, Membership: conf, Timing: testTiming(),
		ClusterVersion: Protocol3, ClusterGeneration: 3,
	}
	installed := newTest(1, testState{term: 1}, 0, false)
	installed.Step(t0, msg(core.MsgHeartbeat, 2, 1, 1))
	installed.Ready()
	installed.InstallSnapshot(meta)
	installed.Ready()
	started := nodeOf(1, 1, conf, Protocol3, 0)
	for name, r := range map[string]*Raft{"installed": installed, "started": started} {
		if !r.conf().IsRetiredKey(keyOf3) {
			t.Fatalf("%s: the snapshot's retired keys are not the node's: %v", name, r.conf().RetiredKeys)
		}
		r.now = t0.Add(10 * time.Second)
		r.term, r.vote = 2, 1
		r.becomeLeader()
		commitLast(r)
		if r.log.committed < r.termStart {
			t.Fatalf("%s: setup: the no-op did not commit", name)
		}
		if code := proposeAddLearner(r, 4, keyOf3); code != codeMembershipInvalid {
			t.Fatalf("%s: a learner was added over a key the snapshot retired: %v", name, code)
		}
		if code := proposeAddLearner(r, 4, [32]byte{4}); code != 0 {
			t.Fatalf("%s: a fresh key was refused: %v", name, code)
		}
	}
}

// A retired key never takes a live member's admission away: a member that
// holds one — admitted before the raise, or named by a forced
// configuration — stays in the admission set, and the transport admits it
// as any member. Only AddLearner and the JOIN read the retired keys.
func TestARetiredKeyNeverCutsALiveMember(t *testing.T) {
	conf := testConf(1, 2, 3)
	conf.RetiredKeys = [][32]byte{keyOf3}
	r := nodeOf(1, 5, conf, Protocol3, 0)
	if k := admittedKey(r, 3); k != keyOf3 {
		t.Fatalf("member 3, which holds a retired key, left the admission set: %x", k[:2])
	}
}

// The issue's stale voter (#648), at protocol 3. A member removed (3)
// leaves its key retired, so its host that rejoins under a new id (4) is
// refused its old key and takes a fresh one. A voter that was down across
// both changes still lists 3 under the old key: the leader record it is
// sent for 4 carries the fresh key, and the admission set it publishes
// holds no key twice, which a transport admits for neither member
// (indexMembers, review M5).
func TestAStaleVoterTakesTheRecordOfALeaderThatRejoinedOverAFreshKey(t *testing.T) {
	lead := keyLeader(Protocol3, 0)
	if err := changeAndCommit(lead, "remove 3", func() error { return proposeRemove(lead, 3) }); err != nil {
		t.Fatal(err)
	}
	if code := proposeAddLearner(lead, 4, keyOf3); code != codeMembershipInvalid {
		t.Fatalf("the host of removed member 3 rejoined as 4 over its old key: %v", code)
	}
	fresh := [32]byte{0x9a}
	if err := changeAndCommit(lead, "add 4", func() error {
		if code := proposeAddLearner(lead, 4, fresh); code != 0 {
			return fmt.Errorf("refused: %v", code)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fconf := lead.conf().Clone()
	for i := range fconf.Members {
		fconf.Members[i].Voter = true
	}
	b := nodeOf(2, 5, fconf, Protocol3, 0)
	hb := msg(core.MsgHeartbeat, 4, 2, 5)
	hb.Commit = 10
	b.Step(b.now, hb)
	b.Ready()
	rec, ok := recordFor(b, core.FlagProtocol2|core.FlagProtocol3)
	if !ok {
		t.Fatal("setup: member 2 sent no leader record for 4")
	}

	a := nodeOf(1, 3, testConf(1, 2, 3), Protocol3, 0)
	a.Step(a.now, rec)
	rd := a.Ready()
	if rd.LeaderHint.ID != 4 {
		t.Fatalf("the stale voter did not take the record for 4: hint %v", rd.LeaderHint.ID)
	}
	seen := map[[32]byte][]core.NodeID{}
	for _, m := range rd.Membership {
		seen[m.SPKI] = append(seen[m.SPKI], m.ID)
	}
	for k, ids := range seen {
		if len(ids) > 1 {
			t.Fatalf("the stale voter publishes key %x under %v: a transport admits it for neither", k[:2], ids)
		}
	}
	if got := seen[fresh]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("the vouched-for leader is not published under its fresh key: %v", got)
	}
}
