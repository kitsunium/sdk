package raft

import (
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// nodeOf is node id at term over a snapshot at index 10 of configuration
// conf, all of it committed, at cluster version cv.
func nodeOf(id core.NodeID, term uint64, conf core.MembershipValue, cv uint32, mut mutation) *Raft {
	cfg := &Config{
		ID: id, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, mut: mut,
	}
	conf.Index = 10
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: term, Commit: 10},
		Snapshot: core.SnapshotMetaValue{
			Position: core.PositionValue{Term: term, Index: 10}, Membership: conf,
			Timing: testTiming(), ClusterVersion: cv, ClusterGeneration: uint64(cv),
		},
		Now: t0,
	})
	r.now = t0.Add(10 * time.Second)
	r.Ready()
	return r
}

// followerOf4 is member 2 of members 1 to 4 following leader 4 at term 5,
// which it heard just now.
func followerOf4(cv uint32) *Raft {
	b := nodeOf(2, 5, testConf(1, 2, 3, 4), cv, 0)
	hb := msg(core.MsgHeartbeat, 4, 2, 5)
	hb.Commit = 10
	b.Step(b.now, hb)
	b.Ready()
	return b
}

// recordFrom is the leader record member 2 sends 1 for leader 4, as
// followerOf4 answers a PreVote of member 1 behind its commit.
func recordFrom(b *Raft) (core.MessageValue, bool) {
	pv := msg(core.MsgPreVote, 1, 2, 6)
	pv.Index, pv.LogTerm = 3, 2
	b.Step(b.now, pv)
	for _, m := range b.Ready().Messages {
		if m.Kind == core.MsgLeaderRecord && m.To == 1 {
			return m, true
		}
	}
	return core.MessageValue{}, false
}

// staleVoter is member 1 of members 1 to 3, which knows no leader at term 3.
func staleVoter(mut mutation) *Raft {
	return nodeOf(1, 3, testConf(1, 2, 3), Protocol2, mut)
}

// admits reports whether r's admission set holds id.
func admits(r *Raft, id core.NodeID) bool {
	return slices.ContainsFunc(r.admitted(), func(m core.MemberValue) bool { return m.ID == id })
}

// checkLeaderRecordReplicationOnly: a leader a member vouched for is
// admitted for its appends and heartbeats only; its vote request is
// dropped unanswered (D4 as arbitrated for #542).
func checkLeaderRecordReplicationOnly(mut mutation) error {
	rec, ok := recordFrom(followerOf4(Protocol2))
	if !ok {
		return fmt.Errorf("setup: no leader record")
	}
	a := staleVoter(mut)
	a.Step(a.now, rec)
	if rd := a.Ready(); !admits(a, 4) || rd.LeaderHint.ID != 4 {
		return fmt.Errorf("setup: the vouched-for leader is not admitted")
	}
	v := msg(core.MsgVote, 4, 1, 6)
	v.Index, v.LogTerm = 50, 5
	a.Step(a.now, v)
	if rd := a.Ready(); granted(rd, core.MsgVoteResp) || a.Status().Term != 3 {
		return fmt.Errorf("a leader admitted by a record alone obtained a vote, or moved the term to %d", a.Status().Term)
	}
	hb := msg(core.MsgHeartbeat, 4, 1, 6)
	a.Step(a.now, hb)
	a.Ready()
	if st := a.Status(); st.Lead != 4 || st.Term != 6 {
		return fmt.Errorf("the heartbeat of the vouched-for leader was not followed: %+v", st)
	}
	return nil
}

func TestALeaderRecordAdmitsForReplicationOnly(t *testing.T) {
	if err := checkLeaderRecordReplicationOnly(0); err != nil {
		t.Fatal(err)
	}
	if err := checkLeaderRecordReplicationOnly(mutHintAnyKind); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// A follower sends the record only at cluster version 2, only while it
// follows a live leader, and only to a member campaigning behind its
// commit; the record is the committed configuration's.
func TestALeaderRecordIsSentOnlyToAStaleMemberAtProtocol2(t *testing.T) {
	rec, ok := recordFrom(followerOf4(Protocol2))
	if !ok {
		t.Fatal("a follower of a live leader sent no record to a member behind its commit")
	}
	if rec.Term != 5 || len(rec.Entries) != 1 {
		t.Fatalf("record %+v", rec)
	}
	if _, ok := recordFrom(followerOf4(1)); ok {
		t.Fatal("a record was sent at cluster version 1")
	}
	b := followerOf4(Protocol2)
	b.now = b.now.Add(10 * time.Second)
	if _, ok := recordFrom(b); ok {
		t.Fatal("a record was sent with no live leader")
	}
	b = followerOf4(Protocol2)
	pv := msg(core.MsgPreVote, 1, 2, 6)
	pv.Index, pv.LogTerm = 10, 5
	b.Step(b.now, pv)
	for _, m := range b.Ready().Messages {
		if m.Kind == core.MsgLeaderRecord {
			t.Fatal("a record was sent to a member not behind the commit")
		}
	}
}

// A record is refused from a non-member, at an older term, for a retired
// or already known ID, for an unpinned key, or while a leader is live; an
// accepted one ends when a configuration lists the leader, when it retires
// it, or when its delay passes.
func TestALeaderRecordIsBoundedAndEnds(t *testing.T) {
	rec, _ := recordFrom(followerOf4(Protocol2))
	for name, mutate := range map[string]func(a *Raft, m *core.MessageValue){
		"from a non-member": func(_ *Raft, m *core.MessageValue) { m.From = 9 },
		"older term":        func(a *Raft, m *core.MessageValue) { a.term = m.Term + 1 },
		"retired id":        func(a *Raft, _ *core.MessageValue) { a.base.Retired = append(a.base.Retired, 4) },
		"live leader": func(a *Raft, _ *core.MessageValue) {
			a.lead, a.lastLeaderContact = 2, a.now
		},
	} {
		a := staleVoter(0)
		m := rec
		mutate(a, &m)
		a.Step(a.now, m)
		if admits(a, 4) {
			t.Fatalf("%s: the record was taken", name)
		}
	}
	a := staleVoter(0)
	a.Step(a.now, rec)
	a.Tick(a.now.Add(hintElectionTimeouts*a.timing.ElectionTimeoutMax - time.Millisecond))
	if !admits(a, 4) {
		t.Fatal("the record ended before its delay")
	}
	a.Tick(a.now.Add(time.Millisecond))
	if admits(a, 4) {
		t.Fatal("the record outlived its delay")
	}
	a = staleVoter(0)
	a.Step(a.now, rec)
	a.base.Retired = append(a.base.Retired, 4)
	a.confChanged()
	if admits(a, 4) || a.hint.ID != 0 {
		t.Fatal("the record outlived the leader's retirement")
	}
}
