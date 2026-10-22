package raft

import (
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #549: a node with no table asks for a snapshot in its answer to a
// heartbeat, not only to an append. A leader whose progress for it had
// match at its last index had nothing to append, sent only heartbeats,
// and the node, answering them plainly, held no state for as long as the
// cluster stayed quiet.
func TestANoTableNodeAsksForASnapshotOnAHeartbeat(t *testing.T) {
	cfg := &Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, NoTable: true,
	}
	base := core.SnapshotMetaValue{Position: core.PositionValue{Term: 2, Index: 10}, Membership: testConf(1, 2, 3)}
	f := NewRaft(cfg, &StartState{HardState: core.HardState{Term: 2, Commit: 10}, Snapshot: base, Now: t0})
	hb := msg(core.MsgHeartbeat, 1, 2, 2)
	hb.Commit = 10
	f.Step(t0.Add(time.Second), hb)
	rd := f.Ready()
	all := append(rd.Messages, rd.AfterSync...)
	var ask core.MessageValue
	k := slices.IndexFunc(all, func(m core.MessageValue) bool {
		return m.Kind == core.MsgAppResp && m.Reject && m.Flags&core.FlagNeedSnapshot != 0
	})
	if k < 0 {
		t.Fatalf("a node with no table answered a heartbeat without asking for a snapshot: %+v", all)
	}
	ask = all[k]
	if !slices.ContainsFunc(all, func(m core.MessageValue) bool { return m.Kind == core.MsgHeartbeatResp }) {
		t.Fatal("the heartbeat itself was not answered: the leader would lose the round")
	}

	// The leader, with nothing to append to the peer, moves it to the
	// snapshot path on that answer.
	l := leaderOf(2, 0, chainEntries(0, 1, 1, 1), 0, false)
	last := l.log.lastIndex()
	ok := msg(core.MsgAppResp, 2, 1, 2)
	ok.Index = last
	l.Step(l.now, ok)
	l.Ready()
	if pr := l.prs[2]; pr == nil || pr.match != last {
		t.Fatalf("setup: peer 2's progress %+v, want match %d", l.prs[2], last)
	}
	ask.Term = 2
	l.Step(l.now, ask)
	if rd := l.Ready(); !slices.Contains(rd.SendSnapshot, 2) {
		t.Fatalf("the leader did not move the peer that asked to the snapshot path: %+v", rd.SendSnapshot)
	}

	// A node with a table answers a heartbeat as before.
	g := newTest(3, testState{term: 2, commit: 0}, 0, false)
	g.Step(t0.Add(time.Second), msg(core.MsgHeartbeat, 1, 3, 2))
	rd = g.Ready()
	if slices.ContainsFunc(append(rd.Messages, rd.AfterSync...), func(m core.MessageValue) bool { return m.Kind == core.MsgAppResp }) {
		t.Fatal("a node with a table answered a heartbeat with an append answer")
	}
}
