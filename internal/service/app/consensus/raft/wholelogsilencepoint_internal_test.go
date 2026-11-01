package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// pointNet is #791's silenceNet with each node's silence condition as its
// driver persists it: the start's index, lowered by a Ready's
// SilenceLowered before that Ready's write reaches the store, 0 once a
// Ready says SilenceEnded. A restart starts the node on it, as silenceOf
// reads SILENT back. lowered keeps, per node, each lowering with the
// truncation the same Ready carried.
type pointNet struct {
	*silenceNet
	point   map[core.NodeID]uint64
	lowered map[core.NodeID][]loweredAt
}

// loweredAt is one Ready that lowered a silence: the new point and the
// index that Ready truncated from, 0 for none.
type loweredAt struct {
	index, truncateFrom uint64
}

// round is silenceNet.round, keeping each node's persisted point.
func (h *pointNet) round() int {
	var out []core.MessageValue
	for _, id := range []core.NodeID{1, 2, 3} {
		r := h.nodes[id]
		if r == nil || h.down[id] || !r.HasReady() {
			continue
		}
		rd := r.Ready()
		h.persist(id, &rd)
		if rd.Seq != 0 {
			r.StorageDone(rd.Seq, r.log.lastIndex(), r.log.lastTerm())
		}
		out = append(out, rd.Messages...)
		out = append(out, rd.AfterSync...)
	}
	n := 0
	for _, m := range out {
		if h.down[m.To] || (h.drop != nil && h.drop(m)) {
			continue
		}
		if r := h.nodes[m.To]; r != nil {
			r.Step(h.now, m)
			n++
		}
	}
	return n
}

// persist does to node id's condition what its driver does with rd.
func (h *pointNet) persist(id core.NodeID, rd *ReadyValue) {
	if rd.SilenceLowered != 0 {
		h.lowered[id] = append(h.lowered[id], loweredAt{index: rd.SilenceLowered, truncateFrom: rd.TruncateFrom})
		if !rd.SilenceEnded {
			h.point[id] = min(h.point[id], rd.SilenceLowered)
		}
	}
	if rd.SilenceEnded {
		h.point[id] = 0
	}
}

func (h *pointNet) settle() {
	for range 200 {
		if h.round() == 0 && h.round() == 0 {
			return
		}
	}
	h.t.Fatal("the network never settled")
}

func (h *pointNet) tick(d time.Duration) {
	h.now = h.now.Add(d)
	for _, id := range []core.NodeID{1, 2, 3} {
		if r := h.nodes[id]; r != nil && !h.down[id] {
			r.Tick(h.now)
		}
	}
	h.settle()
}

// electedWithoutA takes A down and returns the leader B and C elect within
// 30 s, nil when none.
func (h *pointNet) electedWithoutA() *Raft {
	h.down[1] = true
	for range 300 {
		h.tick(100 * time.Millisecond)
		if l := h.leaderAmong(2, 3); l != nil {
			return l
		}
	}
	return nil
}

// olderTailNet is #791's older-tail cluster: C (node 3) holds 1..6 at term
// 1, the tail 4..6 an old leader appended and never committed, its hard
// state at term 2 commit 2, and SILENT(2, 7); A and B hold 1..3. down
// names the nodes that start down.
func olderTailNet(t *testing.T, down ...core.NodeID) *pointNet {
	t.Helper()
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
	h := &pointNet{
		silenceNet: &silenceNet{t: t, down: map[core.NodeID]bool{}, nodes: map[core.NodeID]*Raft{
			1: start(1, chainEntries(0, 1, 1, 1, 1), 1),
			2: start(2, chainEntries(0, 1, 1, 1, 1), 1),
			3: start(3, chainEntries(0, 1, 1, 1, 1, 1, 1, 1), 2),
		}},
		point: map[core.NodeID]uint64{3: 7}, lowered: map[core.NodeID][]loweredAt{},
	}
	for _, id := range down {
		h.down[id] = true
	}
	return h
}

// restartC restarts node 3 on its log, its hard state and the silence
// condition its driver persisted.
func (h *pointNet) restartC() *Raft {
	c := h.nodes[3]
	cfg := quietSilenceCfg(3)
	if p := h.point[3]; p != 0 {
		cfg.SilentTerm, cfg.SilentIndex = c.cfg.SilentTerm, p
	}
	c = NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: c.term, Vote: c.vote, Commit: c.log.committed}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: entriesOf(c), Now: h.now,
	})
	c.Ready()
	h.nodes[3] = c
	return c
}

// #793: lowerSilence lowered the truncation point in memory only, while the
// truncation that justifies it is durable. C accepts A's conflicting append
// at 4: it drops its tail 4..6, appends A's no-op and syncs, and its point
// falls from 7 to 4; then C restarts before the append carrying commit 4
// reaches it (the only frame dropped). Started again on SILENT(2, 7), with
// a log that ends at 4 and no conflict left, C got one no-op of A's, at 5,
// and stayed silent in a cluster that wrote nothing: once A failed, B and C
// elected no one. The lowered point now leaves in the Ready that carries
// the truncation, the driver persists it first, and C restarts on 4.
func TestALoweredSilencePointSurvivesARestartBeforeTheCommit(t *testing.T) {
	h := olderTailNet(t)
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp && m.Commit >= 4 }
	a, c := h.nodes[1], h.nodes[3]
	h.now = t0.Add(3 * time.Second)
	a.Tick(h.now)
	h.settle()
	if a.role != core.RoleLeader || a.term != 2 || a.termStart != 4 || a.log.committed != 4 {
		t.Fatalf("setup: node 1 role %v term %d termStart %d commit %d", a.role, a.term, a.termStart, a.log.committed)
	}
	if t4, _ := c.log.term(4); c.log.lastIndex() != 4 || t4 != 2 || c.cfg.SilentIndex != 4 || c.log.committed >= 4 {
		t.Fatalf("setup: C last %d term(4) %d silent %d/%d commit %d; want the tail dropped, the point lowered, commit below 4",
			c.log.lastIndex(), t4, c.cfg.SilentTerm, c.cfg.SilentIndex, c.log.committed)
	}
	if got := h.lowered[3]; len(got) != 1 || got[0] != (loweredAt{index: 4, truncateFrom: 4}) {
		t.Fatalf("the Readies lowered C's point as %+v, want once to 4, in the Ready that truncates from 4", got)
	}
	if h.point[3] != 4 {
		t.Fatalf("C's persisted point is %d after the conflict, want 4", h.point[3])
	}
	h.drop = nil
	c = h.restartC()
	if c.cfg.SilentIndex != 4 {
		t.Fatalf("C restarted on point %d, want the lowered 4", c.cfg.SilentIndex)
	}
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 || h.point[3] != 0 {
		t.Fatalf("restarted after the lowering, C is still silent %d/%d (persisted %d) after 30 s of a quiet cluster: A last %d commit %d, C last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3], a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	if l := h.electedWithoutA(); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v", h.nodes[2].role, c.role)
	}
}

// A Ready names a lowered point only when the point moved: below it, under
// a leader of a term at least SilentTerm, and always beside the truncation
// that justifies it. A conflict at or past the point, or under an older
// leader, or on a node that is not silent, lowers nothing.
func TestOnlyALoweringLeavesInTheReady(t *testing.T) {
	cases := []struct {
		name                  string
		term, silent, entryAt uint64
		silentIndex           uint64
		want                  uint64
	}{
		{name: "below the point, the leader of SilentTerm", term: 5, silent: 5, entryAt: 4, silentIndex: 7, want: 4},
		{name: "below the point, a later leader", term: 6, silent: 5, entryAt: 4, silentIndex: 7, want: 4},
		{name: "below the point, an older leader", term: 4, silent: 5, entryAt: 4, silentIndex: 7},
		{name: "at the point", term: 5, silent: 5, entryAt: 7, silentIndex: 7},
		{name: "not silent", term: 5, entryAt: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := quietSilenceCfg(3)
			cfg.SilentTerm, cfg.SilentIndex = tc.silent, tc.silentIndex
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
			rd := r.Ready()
			if rd.TruncateFrom != tc.entryAt {
				t.Fatalf("setup: the append truncated from %d, want %d", rd.TruncateFrom, tc.entryAt)
			}
			if rd.SilenceLowered != tc.want {
				t.Fatalf("the Ready lowers the point to %d (SilentIndex now %d), want %d", rd.SilenceLowered, r.cfg.SilentIndex, tc.want)
			}
		})
	}
}
