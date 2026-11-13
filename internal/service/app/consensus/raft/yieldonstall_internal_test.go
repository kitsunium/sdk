package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The file is named to sort after every seed suite: Bazel deals raft_test's
// shards TOP-LEVEL tests by index, and a test placed before a seed part
// moves it to another shard (CLAUDE.md, #1112).

// #554 as a named schedule: the leader's disk stalls for three maximum
// election timeouts, every sync it issues blocked, while its followers'
// disks answer. It still has contact with a majority, so CheckQuorum keeps
// it leading; its durable index stops moving, and within
// ElectionTimeoutMax the driver's watch (simOptions.watch) hands its
// leadership over to a follower, which commits the writes without it
// (D13). The negative control runs the same seed without the watch: the
// stalled leader then keeps leading, as it did before #554.
func TestSimStalledDiskLeaderHandsOver(t *testing.T) {
	eachSeed(t, seeds(t, 20), func(t *testing.T, seed uint64) {
		s, old, handedAt, from := stallLeader(t, seed, true)
		stall := 3 * s.timing.ElectionTimeoutMax
		if handedAt.IsZero() {
			t.Fatalf("seed %d: leader %d kept its leadership through a %v stall of its disk", seed, old.id, stall)
		}
		// The watch fires ElectionTimeoutMax after the durable index stopped,
		// within a tick and the write in flight when the stall began.
		if d := handedAt.Sub(from); d > s.timing.ElectionTimeoutMax+time.Second {
			t.Fatalf("seed %d: the hand-over came %v into the stall", seed, d)
		}
		if l := s.leader(); l == nil || l.id == old.id {
			t.Fatalf("seed %d: no other node leads at the end of the stall", seed)
		}
		ctl, ctlOld, ctlHanded, _ := stallLeader(t, seed, false)
		if !ctlHanded.IsZero() || ctl.leader() != ctlOld {
			t.Fatalf("seed %d: without the watch the stalled leader still handed over: the stall does not stall", seed)
		}
	})
}

// stallLeader runs seed for 5 s of load, then stalls the leader's disk for
// three maximum election timeouts under load, the driver's progress watch
// on or off. It returns the simulation, the stalled leader, the instant it
// sent its TimeoutNow (zero for none) and the stall's start, and fails the
// test on a violation or when nothing committed during the stall.
func stallLeader(t *testing.T, seed uint64, watch bool) (*sim, *simNode, time.Time, time.Time) {
	s := newSim(simOptions{seed: seed, watch: watch})
	s.play(nil, 5*time.Second, true)
	old := s.leader()
	if s.err != nil || old == nil {
		t.Fatalf("seed %d: no leader before the stall: %v", seed, s.err)
	}
	var handedAt time.Time
	s.onSend = func(m core.MessageValue) {
		if m.Kind == core.MsgTimeoutNow && m.From == old.id && handedAt.IsZero() {
			handedAt = s.now
		}
	}
	from, before := s.now, s.maxCommit()
	s.play(schedule{{kind: nemStallDisk, nodes: []core.NodeID{old.id}}}, 3*s.timing.ElectionTimeoutMax, true)
	if s.err != nil {
		t.Fatalf("seed %d (watch %v): %v", seed, watch, s.err)
	}
	if s.maxCommit() <= before {
		t.Fatalf("seed %d (watch %v): nothing committed during the stall (commit %d)", seed, watch, before)
	}
	return s, old, handedAt, from
}

// The schedule #924 names, composed in one literal: node 1 cut from {2, 3}
// for 3 s, node 2's disk stalled for 10 s, then node 3's clock 2 % fast,
// under load. Safety holds throughout, and the window, which ends healed,
// is followed by progress on every node.
func TestSimComposedScheduleKeepsSafetyAndProgress(t *testing.T) {
	eachSeed(t, seeds(t, 10), func(t *testing.T, seed uint64) {
		s := newSim(simOptions{seed: seed, watch: true})
		s.play(nil, 3*time.Second, true)
		s.play(schedule{
			{kind: nemPartition, nodes: []core.NodeID{1}, dur: 3 * time.Second},
			{kind: nemStallDisk, nodes: []core.NodeID{2}, from: time.Second, dur: 10 * time.Second},
			{kind: nemSkew, nodes: []core.NodeID{3}, from: 11 * time.Second, param: 1.02},
		}, 15*time.Second, true)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		before := s.maxCommit()
		s.play(nil, 5*time.Second, true)
		s.play(nil, 3*time.Second, false)
		if s.err != nil {
			t.Fatalf("seed %d after the window: %v", seed, s.err)
		}
		for _, id := range s.ids {
			if c := s.nodes[id].r.Status().Commit; c <= before || c != s.maxCommit() {
				t.Fatalf("seed %d: node %d at commit %d (before %d, max %d)", seed, id, c, before, s.maxCommit())
			}
		}
	})
}
