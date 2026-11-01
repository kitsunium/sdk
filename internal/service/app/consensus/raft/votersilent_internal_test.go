package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// silentVoterBeforeRemove is #790's scenario up to the operator's Remove,
// under MinVoters 2. Node 1 (A) leads term 2 of voters 1, 2 and 3, and
// entries 1..11 are committed and held by all three. Node 3 (C) crashes; a
// record past its last entry is damaged, so TruncateAndStaySilent drops no
// entry and C restarts SILENT(2, 12), reopenSilenced's last+1, with entries
// 1..11. C answers A's heartbeats, but acknowledges no append until A
// commits an entry of its term past 11, and none of A's appends reaches it
// yet (the network drops them; a no-op A appends for it, #787, waits with
// them). Every node is a real step function; nothing is forced.
func silentVoterBeforeRemove(t *testing.T) *silenceNet {
	t.Helper()
	cfgOf := func(id core.NodeID) *Config {
		return &Config{
			ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
			MaxBatchBytes: 150, MinVoters: 2, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		}
	}
	start := func(id core.NodeID) *Raft {
		return NewRaft(cfgOf(id), &StartState{
			HardState: core.HardState{Term: 1, Commit: 2}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
			Entries: chainEntries(0, 1, 1, 1), Now: t0,
		})
	}
	h := &silenceNet{t: t, nodes: map[core.NodeID]*Raft{1: start(1), 2: start(2), 3: start(3)}, down: map[core.NodeID]bool{}}
	a := h.nodes[1]
	h.now = t0.Add(3 * time.Second)
	a.Tick(h.now)
	h.settle()
	if a.role != core.RoleLeader || a.term != 2 {
		t.Fatalf("setup: node 1 role %v term %d", a.role, a.term)
	}
	for i := 4; i <= 11; i++ {
		if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(i)}); code != 0 {
			t.Fatalf("propose %d: %v", i, code)
		}
		h.settle()
	}
	h.tick(100 * time.Millisecond)
	c := h.nodes[3]
	if a.log.committed != 11 || c.log.lastIndex() != 11 || c.log.committed != 11 {
		t.Fatalf("setup: A commit %d, C last %d commit %d", a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	ents := make([]core.EntryValue, 0, 11)
	for i := uint64(1); i <= 11; i++ {
		ents = append(ents, *c.log.at(i))
	}
	h.down[3] = true
	for range 50 {
		h.tick(100 * time.Millisecond)
	}
	ccfg := cfgOf(3)
	ccfg.SilentTerm, ccfg.SilentIndex = c.term, 12
	c = NewRaft(ccfg, &StartState{
		HardState: core.HardState{Term: c.term, Vote: c.vote, Commit: 11}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: ents, Now: h.now,
	})
	c.Ready()
	h.nodes[3] = c
	h.down[3] = false
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp }
	for range 3 { // C answers A's heartbeats: A hears it
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex == 0 || !a.answering(3) {
		t.Fatalf("setup: C silent %d/%d, answering at A %v", c.cfg.SilentTerm, c.cfg.SilentIndex, a.answering(3))
	}
	return h
}

// #790: the strict reconfiguration check counted a silent voter as one that
// answers. It answers every heartbeat, but acknowledges no append until the
// leader commits an entry of its term past its truncation point. Removing
// B, alive, was admitted over voters {A, C}: the configuration governs from
// its append (D8), so the removal needed C's acknowledgement, which waited
// for a commit the removal itself blocked, and nothing committed again, all
// three nodes up. The removal is refused while C is silent, and admitted
// once C's silence ended; it then commits.
func TestARemovalThatLeavesASilentVoterInTheMajorityIsRefused(t *testing.T) {
	h := silentVoterBeforeRemove(t)
	a, c := h.nodes[1], h.nodes[3]
	if err := proposeRemove(a, 2); err == nil {
		removalAt := a.conf().Index
		for range 100 {
			h.tick(100 * time.Millisecond)
		}
		t.Fatalf("Remove(2) admitted at index %d over voters {1, 3} with 3 silent: 10 s later A commit %d, C silent %d/%d",
			removalAt, a.log.committed, c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
	// One write, committed by A and B, ends C's silence once A's appends
	// reach it; the removal is then admitted, and commits with C's
	// acknowledgement.
	h.drop = nil
	if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(12)}); code != 0 {
		t.Fatalf("propose 12: %v", code)
	}
	for range 10 {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 {
		t.Fatalf("C still silent %d/%d after a committed write", c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
	if err := proposeRemove(a, 2); err != nil {
		t.Fatalf("Remove(2) refused once C's silence ended: %v", err)
	}
	for range 10 {
		h.tick(100 * time.Millisecond)
	}
	if a.log.committed < a.conf().Index || len(a.confAt(a.log.committed).Voters()) != 2 {
		t.Fatalf("the removal at %d did not commit: A commit %d, committed voters %v", a.conf().Index, a.log.committed,
			a.confAt(a.log.committed).Voters())
	}
}

// fiveVotersTwoSilent is leader 1 of voters 1 to 5 at term 6: the voters
// acked acknowledged its no-op, voters 4 and 5 answer its heartbeats as
// silent nodes do (FlagTruncated) and acknowledge nothing, and any other
// voter never answered.
func fiveVotersTwoSilent(acked ...core.NodeID) *Raft {
	r := nodeOf(1, 5, testConf(1, 2, 3, 4, 5), Protocol2, 0)
	r.term, r.vote = 6, 1
	r.becomeLeader()
	r.StorageDone(r.Ready().Seq, r.log.lastIndex(), 6)
	for _, id := range acked {
		ack := msg(core.MsgAppResp, id, 1, 6)
		ack.Index = r.log.lastIndex()
		r.Step(r.now, ack)
	}
	for _, id := range []core.NodeID{4, 5} {
		hb := msg(core.MsgHeartbeatResp, id, 1, 6)
		hb.Flags, hb.RejectHint, hb.Commit = core.FlagTruncated, 10, 10
		r.Step(r.now, hb)
	}
	r.Ready()
	return r
}

// #790's variant under MinVoters 3: of five voters, two silent in the same
// quiet window. Removing a live voter leaves four, three of which must
// acknowledge, and only the leader and one other can: refused. Removing a
// silent voter leaves a majority that acknowledges: admitted. So is the
// first removal once the silent voters answer without the flag. A move and
// a DenyPin take the same check, and are judged alike.
func TestAChangeIsJudgedWithoutTheSilentVoters(t *testing.T) {
	if err := proposeRemove(fiveVotersTwoSilent(2, 3), 2); err == nil {
		t.Fatal("Remove(2) admitted: of voters {1, 3, 4, 5}, 4 and 5 are silent, and no majority acknowledges")
	}
	if err := proposeRemove(fiveVotersTwoSilent(2, 3), 4); err != nil {
		t.Fatalf("the removal of silent voter 4 was refused: %v", err)
	}
	if err := proposeDenyPin(fiveVotersTwoSilent(2, 3), 3); err == nil {
		t.Fatal("DenyPin(3) admitted: without 3, only voters 1 and 2 of five acknowledge")
	}
	if code := proposeMove(fiveVotersTwoSilent(2), 1, "a1-moved"); code == 0 {
		t.Fatal("the leader's move admitted: only voters 1 and 2 of five acknowledge")
	}
	r := fiveVotersTwoSilent(2, 3)
	for _, id := range []core.NodeID{4, 5} {
		r.Step(r.now, msg(core.MsgHeartbeatResp, id, 1, 6))
	}
	if err := proposeRemove(r, 2); err != nil {
		t.Fatalf("Remove(2) refused once 4 and 5 answer without the silence flag: %v", err)
	}
}
