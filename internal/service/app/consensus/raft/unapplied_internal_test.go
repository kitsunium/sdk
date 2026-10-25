package raft

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// boundedFollower is node 2 of members 1, 2, 3 at term 1, its apply bounded
// by limit bytes and its payloads in memory by recent, holding the commands
// 1..n of term 1, durable and committed by leader 1's append: what a
// follower slower to apply than its leader holds once its apply goroutine
// reported lag bytes unapplied (#664).
func boundedFollower(mut mutation, limit, recent, lag int64, n int) *Raft {
	f := NewRaft(&Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		MaxApplyLagBytes: limit, RecentEntriesBytes: recent, mut: mut,
	}, &StartState{HardState: core.HardState{Term: 1}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)}, Now: t0})
	terms := make([]uint64, n)
	for i := range terms {
		terms[i] = 1
	}
	f.SetApplyLag(lag, false)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Commit, m.Entries = uint64(n), chainEntries(0, 1, terms...)
	f.Step(t0, m)
	rd := f.Ready()
	f.StorageDone(rd.Seq, uint64(n), 1)
	return f
}

// payloadOf sums the payload bytes of es.
func payloadOf(es []core.EntryValue) int64 {
	var b int64
	for i := range es {
		b += int64(len(es[i].Data))
	}
	return b
}

// checkApplyBounded: a follower's Ready hands apply no more than
// MaxApplyLagBytes past what apply reports it holds, one entry when it
// holds nothing, and the rest once apply reports room again; a follower
// slower to apply than its leader held everything it was behind by (#664,
// mutApplyUnbounded).
func checkApplyBounded(mut mutation) error {
	const limit = 32
	f := boundedFollower(mut, limit, 0, 0, 20)
	var handed int64
	for f.HasReady() {
		handed += payloadOf(f.Ready().Apply)
	}
	if handed == 0 || handed > limit {
		return fmt.Errorf("a follower whose apply held nothing was handed %d payload bytes at once, its bound is %d", handed, limit)
	}
	// Apply reports it holds them all still: nothing more is handed.
	f.SetApplyLag(handed, false)
	for f.HasReady() {
		if b := payloadOf(f.Ready().Apply); b > 0 {
			return fmt.Errorf("a follower whose apply held %d bytes, the next entry past its bound of %d, was handed %d more", handed, limit, b)
		}
	}
	// Then it applied them: the next window is handed, in order.
	next := f.log.applied + 1
	f.SetApplyLag(0, false)
	rd := f.Ready()
	if len(rd.Apply) == 0 || rd.Apply[0].Position.Index != next || payloadOf(rd.Apply) > limit {
		return fmt.Errorf("once apply caught up the follower handed %d entries from %v, want the next window from %d", len(rd.Apply), rd.Apply, next)
	}
	return nil
}

// checkHeldPayloadsEvicted: the committed entries a follower holds back
// from apply drop their payloads past RecentEntriesBytes, durable as they
// are; held with their payloads, the entries a follower was behind by
// stayed in memory whatever its apply lag (#664, mutEvictAppliedOnly).
func checkHeldPayloadsEvicted(mut mutation) error {
	const recent = 30
	f := boundedFollower(mut, 16, recent, 16, 20)
	for f.HasReady() {
		if rd := f.Ready(); len(rd.Apply) != 0 || rd.ApplyRead.Lo != 0 {
			return fmt.Errorf("setup: a follower whose apply is at its bound was handed %+v", rd.ApplyRead)
		}
	}
	f.Ready()
	if f.log.window > recent+64 {
		return fmt.Errorf("a follower holding back 20 committed entries from apply kept %d payload bytes in memory, its RecentEntriesBytes is %d", f.log.window, recent)
	}
	return nil
}

// checkNothingHandedBehindARead: once Ready handed entries by index, for the
// driver to read back from the log, nothing is handed in memory until the
// driver says the read is over; queued behind it, entries held their
// payloads for as long as the read lasted (#664, mutApplyBehindRead).
func checkNothingHandedBehindARead(mut mutation) error {
	f := boundedFollower(mut, 1<<20, 30, 1<<20, 20)
	f.Ready()
	if f.log.evicted == 0 || f.log.evicted >= 20 {
		return fmt.Errorf("setup: evicted through %d of 20", f.log.evicted)
	}
	evicted := f.log.evicted
	f.SetApplyLag(0, false)
	rd := f.Ready()
	if rd.ApplyRead != (ReadBackSpec{Lo: 1, Hi: evicted + 1}) || len(rd.Apply) != 0 {
		return fmt.Errorf("the entries evicted through %d were handed as a read of %+v and %d in memory", evicted, rd.ApplyRead, len(rd.Apply))
	}
	for f.HasReady() {
		if rd := f.Ready(); len(rd.Apply) != 0 {
			return fmt.Errorf("entries from %d were handed in memory while apply still read back through %d", rd.Apply[0].Position.Index, evicted)
		}
	}
	f.SetApplyLag(0, false)
	rd = f.Ready()
	if len(rd.Apply) == 0 || rd.Apply[0].Position.Index != evicted+1 || rd.Apply[len(rd.Apply)-1].Position.Index != 20 {
		return fmt.Errorf("once the read was over, the follower handed %d entries in memory, want %d..20", len(rd.Apply), evicted+1)
	}
	return nil
}

// checkNoTableKeepsCommittedPayloads: a node with no table keeps the
// payloads of the entries it holds committed, whose engine effects its
// install replays from them over the snapshot's state (#416); dropped past
// RecentEntriesBytes, the alarm committed past the snapshot was forgotten
// (mutNoTableEvictsCommitted).
func checkNoTableKeepsCommittedPayloads(mut mutation) error {
	cfg := &Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, NoTable: true,
		RecentEntriesBytes: 1, MaxApplyLagBytes: 1 << 20, mut: mut,
	}
	all := chainEntries(0, 1, 1, 1, 1)
	e4 := engineEntry(all[2].ChainSum, 4, core.EntryNoSpaceRaise, &wire.AdminValue{})
	ents := append(append(all, e4), chainEntries(e4.ChainSum, 5, 1)...)
	base := core.SnapshotMetaValue{Position: core.PositionValue{Term: 1, Index: 2}, ChainTip: all[1].ChainSum, Membership: testConf(1, 2, 3)}
	f := NewRaft(cfg, &StartState{HardState: core.HardState{Term: 1, Commit: 5}, Snapshot: base, Entries: ents[2:], Now: t0})
	f.Ready()
	f.lead, f.term = 1, 1
	f.InstallSnapshot(core.SnapshotMetaValue{Position: core.PositionValue{Term: 1, Index: 3}, ChainTip: all[2].ChainSum, Membership: testConf(1, 2, 3), Timing: testTiming()})
	if f.NoTable() || f.log.snapIndex != 3 {
		return fmt.Errorf("setup: the install was not taken: noTable %v base %d", f.NoTable(), f.log.snapIndex)
	}
	if st := f.Status(); st.NoSpace != 4 {
		return fmt.Errorf("the alarm committed at 4, past the snapshot at 3, was forgotten by a node with no table that dropped its payload: noSpace %d", st.NoSpace)
	}
	return nil
}

// A node's apply bounded by MaxApplyLagBytes, slower than its log, with
// RecentEntriesBytes small: the entries it falls behind by are evicted and
// read back from the disk model, through partitions, crashes, restarts that
// load the tail without its payloads, snapshots, lost snapshots and
// installs. Safety and progress hold, every node applies every index once
// and in order, and no Ready hands more than the bound (#663, #664). The
// 24 seeds in three top-level tests (seedPart), named after the
// simulator's so the shards of the seed parts stay where they are.
func TestSimBoundedApplyP1(t *testing.T) { simBoundedApply(t, seedPart(t, 24, 0, 3)) }

func TestSimBoundedApplyP2(t *testing.T) { simBoundedApply(t, seedPart(t, 24, 1, 3)) }

func TestSimBoundedApplyP3(t *testing.T) { simBoundedApply(t, seedPart(t, 24, 2, 3)) }

func simBoundedApply(t *testing.T, seeds []uint64) {
	var reads atomic.Int64
	t.Cleanup(func() {
		if len(seeds) > 0 && !t.Failed() && reads.Load() == 0 {
			t.Error("apply never read an entry back from the log")
		}
	})
	eachSeed(t, seeds, func(t *testing.T, seed uint64) {
		const recent, lag = 96, 64
		s := newSim(simOptions{seed: seed, drop: 0.02, crashRate: 0.5, recent: recent, applyLag: lag, snapEvery: 48, snapLoss: 0.3})
		for k := 0; k < 4 && s.err == nil; k++ {
			if k%2 == 0 {
				s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
			} else {
				s.heal()
			}
			s.run(s.after(3*time.Second), true)
		}
		s.heal()
		s.opt.crashRate, s.opt.drop = 0, 0
		for _, id := range s.ids {
			if !s.nodes[id].alive {
				s.start(s.nodes[id])
			}
		}
		s.run(s.after(6*time.Second), true)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		want := s.maxCommit()
		s.run(s.after(4*time.Second), false)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		reads.Add(int64(s.reads))
		for _, id := range s.ids {
			n := s.nodes[id]
			st := n.r.Status()
			if st.Commit < want {
				t.Fatalf("seed %d: node %d stuck at commit %d < %d", seed, id, st.Commit, want)
			}
			last := n.appliedFrom
			if k := len(n.applied); k > 0 {
				last = n.applied[k-1].Position.Index
			}
			if !n.noTable && (last != st.Commit || len(n.applyQ) != 0 || n.queued != 0) {
				t.Fatalf("seed %d: node %d applied through %d of a commit of %d, %d items still queued (%d bytes)", seed, id, last, st.Commit, len(n.applyQ), n.queued)
			}
			if n.r.log.window > recent+64 && n.r.log.evicted < min(n.r.log.committed, n.r.log.stable) {
				t.Fatalf("seed %d: node %d holds %d payload bytes", seed, id, n.r.log.window)
			}
		}
	})
}
