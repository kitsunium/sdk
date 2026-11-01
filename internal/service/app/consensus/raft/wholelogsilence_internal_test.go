package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// quietSilenceCfg is the configuration of every node of #787's network.
func quietSilenceCfg(id core.NodeID) *Config {
	return &Config{
		ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 150, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
	}
}

// silentOverTheWholeLog is #787's scenario up to the victim's restart.
// Node 1 (A) leads term 2; entries 4..11 are committed and held by all
// three. Node 3 (C) crashes; a record past its last entry is damaged, so
// TruncateAndStaySilent drops no entry and C restarts SILENT(silentTerm,
// 12), reopenSilenced's last+1, with entries 1..11: it holds the leader's
// whole log. silentTerm is C's term, or one no leader reaches, the term an
// unreadable condition reads as. Every node is a real step function;
// nothing is forced, and nothing is written after C's restart.
func silentOverTheWholeLog(t *testing.T, silentTerm uint64) *silenceNet {
	t.Helper()
	start := func(id core.NodeID) *Raft {
		return NewRaft(quietSilenceCfg(id), &StartState{
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
	if a.log.committed != 11 || c.log.lastIndex() != 11 || c.log.committed != 11 || a.prs[3].match != 11 {
		t.Fatalf("setup: A commit %d, C last %d commit %d, C's match at A %d", a.log.committed, c.log.lastIndex(), c.log.committed, a.prs[3].match)
	}
	ents := make([]core.EntryValue, 0, 11)
	for i := uint64(1); i <= 11; i++ {
		ents = append(ents, *c.log.at(i))
	}
	h.down[3] = true
	for range 50 {
		h.tick(100 * time.Millisecond)
	}
	if a.role != core.RoleLeader || a.term != 2 {
		t.Fatalf("setup: A lost its leadership while C was down (role %v term %d)", a.role, a.term)
	}
	ccfg := quietSilenceCfg(3)
	ccfg.SilentTerm, ccfg.SilentIndex = silentTerm, 12
	c = NewRaft(ccfg, &StartState{
		HardState: core.HardState{Term: c.term, Vote: c.vote, Commit: 11}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: ents, Now: h.now,
	})
	c.Ready()
	h.nodes[3] = c
	h.down[3] = false
	return h
}

// electedWithoutA takes A down and returns the leader B and C elect within
// 30 s, nil when none.
func electedWithoutA(h *silenceNet) *Raft {
	h.down[1] = true
	for range 300 {
		h.tick(100 * time.Millisecond)
		if l := h.leaderAmong(2, 3); l != nil {
			return l
		}
	}
	return nil
}

// #787: a node TruncateAndStaySilent made silent without dropping an entry
// holds the leader's whole log, and its silence needs an entry of a term at
// least its own past its truncation point, committed. In a cluster that
// wrote nothing the leader had nothing to send it, and it stayed silent for
// as long as nobody wrote: one more failure then left two live voters of
// three that elected no one. The leader appends a no-op of its term for it,
// which B and A commit; C ends its silence holding every entry, and B and C
// elect a leader once A fails.
func TestASilentNodeHoldingTheWholeLogEndsItsSilenceInAQuietCluster(t *testing.T) {
	h := silentOverTheWholeLog(t, 2)
	a, c := h.nodes[1], h.nodes[3]
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 {
		t.Fatalf("after 30 s of a quiet cluster C is still silent %d/%d, holding A's whole log (A last %d commit %d, C last %d commit %d)",
			c.cfg.SilentTerm, c.cfg.SilentIndex, a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	if a.log.lastIndex() != 12 || c.log.committed != 12 {
		t.Fatalf("the silence ended with A last %d and C commit %d, want one no-op at 12, committed", a.log.lastIndex(), c.log.committed)
	}
	if e := a.log.at(12); e.Kind != core.EntryNoOp || e.Position.Term != 2 {
		t.Fatalf("entry 12 is kind %v of term %d, want a no-op of term 2", e.Kind, e.Position.Term)
	}
	if l := electedWithoutA(h); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v silent %d/%d",
			h.nodes[2].role, c.role, c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
}

// The no-op is bounded: one for a silence that never ends, here an
// unreadable condition no term reaches, and none for a quiet cluster with
// no silent node.
func TestTheLeaderAppendsOneNoOpPerSilence(t *testing.T) {
	h := silentOverTheWholeLog(t, 1<<62)
	a, c := h.nodes[1], h.nodes[3]
	for range 300 {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex == 0 || a.log.lastIndex() != 12 || a.log.committed != 12 {
		t.Fatalf("over 30 s: C silent %v, A last %d commit %d; want C silent, and one no-op, committed", c.cfg.SilentIndex != 0, a.log.lastIndex(), a.log.committed)
	}
	h.nodes[3] = NewRaft(quietSilenceCfg(3), &StartState{
		HardState: core.HardState{Term: c.term, Commit: c.log.committed}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: entriesOf(c), Now: h.now,
	})
	h.nodes[3].Ready()
	for range 300 {
		h.tick(100 * time.Millisecond)
	}
	if a.log.lastIndex() != 12 {
		t.Fatalf("with no silent node A appended up to %d in a quiet cluster", a.log.lastIndex())
	}
}

// entriesOf is r's log, from its first index.
func entriesOf(r *Raft) []core.EntryValue {
	out := make([]core.EntryValue, 0, r.log.lastIndex())
	for i := r.log.snapIndex + 1; i <= r.log.lastIndex(); i++ {
		out = append(out, *r.log.at(i))
	}
	return out
}

// #787's wider case: C restarts silent over a tail of an older term that
// the leader's log never held, entries 4..6 an old leader appended and
// never committed, so its truncation point, 7, lies past the leader's whole
// log. A's term starts at 4: its no-op there conflicts with C's entry 4,
// and C drops its tail. The conflict proves that no entry C held from 4 on
// ever counted towards a commit (Log Matching), so its truncation point
// becomes 4, the leader's commit of its no-op ends the silence, and no
// write is needed to cross 7.
func TestASilentNodeOverAnOlderTermsTailEndsItsSilenceInAQuietCluster(t *testing.T) {
	start := func(id core.NodeID, ents []core.EntryValue, term uint64) *Raft {
		cfg := quietSilenceCfg(id)
		if id == 3 {
			cfg.SilentTerm, cfg.SilentIndex = 2, 7
		}
		return NewRaft(cfg, &StartState{
			HardState: core.HardState{Term: term, Commit: 2}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
			Entries: ents, Now: t0,
		})
	}
	h := &silenceNet{t: t, down: map[core.NodeID]bool{}, nodes: map[core.NodeID]*Raft{
		1: start(1, chainEntries(0, 1, 1, 1, 1), 1),
		2: start(2, chainEntries(0, 1, 1, 1, 1), 1),
		3: start(3, chainEntries(0, 1, 1, 1, 1, 1, 1, 1), 2),
	}}
	a, c := h.nodes[1], h.nodes[3]
	h.now = t0.Add(3 * time.Second)
	a.Tick(h.now)
	h.settle()
	if a.role != core.RoleLeader || a.term != 2 || a.termStart != 4 {
		t.Fatalf("setup: node 1 role %v term %d, its term starting at %d", a.role, a.term, a.termStart)
	}
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 {
		t.Fatalf("after 30 s of a quiet cluster C is still silent %d/%d: A last %d commit %d, C last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	for i := uint64(1); i <= a.log.committed; i++ {
		want, _ := a.log.term(i)
		if got, ok := c.log.term(i); !ok || got != want {
			t.Fatalf("C's silence ended with index %d at term %d, A's is %d", i, got, want)
		}
	}
	if l := electedWithoutA(h); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v", h.nodes[2].role, c.role)
	}
}

// The conflict lowers the truncation point only when it lies below it and
// comes from a leader of a term at least SilentTerm: the leader of an older
// term may lack what the node acknowledged under a later one, and a
// conflict at or past the point says nothing of the entries before it.
func TestAConflictLowersTheTruncationPointOnlyUnderALeaderOfItsTerm(t *testing.T) {
	cases := []struct {
		name               string
		term, silent       uint64
		wantIndex, entryAt uint64
	}{
		{name: "below the point, the leader of SilentTerm", term: 5, silent: 5, entryAt: 4, wantIndex: 4},
		{name: "below the point, a later leader", term: 6, silent: 5, entryAt: 4, wantIndex: 4},
		{name: "below the point, an older leader", term: 4, silent: 5, entryAt: 4, wantIndex: 7},
		{name: "at the point", term: 5, silent: 5, entryAt: 7, wantIndex: 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := quietSilenceCfg(3)
			cfg.SilentTerm, cfg.SilentIndex = tc.silent, 7
			held := chainEntries(0, 1, 1, 1, 1, 1, 1, 1, 1, 1)[:6] // 1..6 at term 1
			if tc.entryAt == 7 {
				held = chainEntries(0, 1, 1, 1, 1, 1, 1, 1, 1, 1)[:8] // 1..8: index 7 is held
			}
			r := NewRaft(cfg, &StartState{
				HardState: core.HardState{Term: tc.term, Commit: 2}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
				Entries: held, Now: t0,
			})
			r.Ready()
			prev := tc.entryAt - 1
			app := msg(core.MsgApp, 1, 3, tc.term)
			app.Index, app.LogTerm, app.Commit = prev, 1, 2
			app.Entries = chainEntries(r.log.chain(prev), tc.entryAt, tc.term)
			r.Step(t0, app)
			if r.log.lastIndex() != tc.entryAt {
				t.Fatalf("setup: the append was not taken (last %d)", r.log.lastIndex())
			}
			if r.cfg.SilentIndex != tc.wantIndex {
				t.Fatalf("SilentIndex %d after a conflict at %d under a leader of term %d (SilentTerm %d), want %d",
					r.cfg.SilentIndex, tc.entryAt, tc.term, tc.silent, tc.wantIndex)
			}
		})
	}
}
