package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A start whose driver loaded the committed commands without their
// payloads, past RecentEntriesBytes (#663): the engine entries among them
// keep theirs, and their effects hold from the start; the window counts
// only the payloads past them; the first Ready hands them by index, for the
// driver to read back, and the entries in memory follow once it read them,
// every index once and in order.
func TestAStartWithEvictedPayloadsHandsThemByIndex(t *testing.T) {
	terms := make([]uint64, 15)
	for i := range terms {
		terms[i] = 1
	}
	cmds := chainEntries(0, 1, terms...)
	e5 := engineEntry(cmds[3].ChainSum, 5, core.EntryNoSpaceRaise, &wire.AdminValue{})
	ents := append(append(cmds[:4:4], e5), chainEntries(e5.ChainSum, 6, terms[5:]...)...)
	var resident int64
	for i := range ents {
		if ents[i].Position.Index <= 10 {
			if ents[i].Kind == core.EntryCommand {
				ents[i].Data = nil
			}
			continue
		}
		resident += int64(len(ents[i].Data))
	}
	f := NewRaft(&Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		RecentEntriesBytes: 1 << 20, MaxApplyLagBytes: 1 << 20,
	}, &StartState{
		HardState: core.HardState{Term: 1, Commit: 15}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: ents, Evicted: 10, Now: t0,
	})
	if st := f.Status(); st.NoSpace != 5 {
		t.Fatalf("the alarm committed at 5, loaded with its payload, is not raised: noSpace %d", st.NoSpace)
	}
	if f.log.window != resident || f.log.evicted != 10 {
		t.Fatalf("window %d evicted %d, want %d bytes past 10", f.log.window, f.log.evicted, resident)
	}
	if _, ok := f.Entry(7); ok {
		t.Fatal("Entry answered an index whose payload was not loaded")
	}
	rd := f.Ready()
	if rd.ApplyRead != (ReadBackSpec{Lo: 1, Hi: 11}) || len(rd.Apply) != 0 {
		t.Fatalf("the first Ready handed a read of %+v and %d entries in memory, want [1, 11) alone", rd.ApplyRead, len(rd.Apply))
	}
	if !f.ApplyHeld() || f.HasReady() {
		t.Fatalf("while apply reads back, entries 11..15 are handed or not held: held %v ready %v", f.ApplyHeld(), f.HasReady())
	}
	f.SetApplyLag(0, false)
	rd = f.Ready()
	if len(rd.Apply) != 5 || rd.Apply[0].Position.Index != 11 || payloadOf(rd.Apply) != resident || f.ApplyHeld() {
		t.Fatalf("once the read was over, %d entries were handed from %v, want 11..15 with their payloads", len(rd.Apply), rd.Apply)
	}
}

// A cluster whose apply is bounded catches up after its nodes restarted on
// a log they load mostly without payloads: no seed may leave a node unable
// to apply what it committed.
func TestSimBoundedApplyRestartsOnALongTail(t *testing.T) {
	s := newSim(simOptions{seed: 7, recent: 64, applyLag: 48})
	s.run(s.after(4*time.Second), true)
	for _, id := range s.ids {
		s.crash(id)
	}
	for _, id := range s.ids {
		s.start(s.nodes[id])
	}
	s.run(s.after(6*time.Second), false)
	if s.err != nil {
		t.Fatal(s.err)
	}
	if s.reads == 0 {
		t.Fatal("setup: no node read back an entry its start loaded without its payload")
	}
	for _, id := range s.ids {
		n := s.nodes[id]
		if got, want := len(n.applied), int(n.r.Status().Commit); got != want || want < 100 {
			t.Fatalf("node %d applied %d of %d committed entries after the restart", id, got, want)
		}
	}
}
