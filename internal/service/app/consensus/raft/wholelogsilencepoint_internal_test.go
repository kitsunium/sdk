package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// pointNet is #791's silenceNet with each node's silence condition as its
// driver persists it: the start's index, lowered by a Ready's
// SilenceLowered before that Ready's write reaches the store, 0 once a
// Ready says SilenceEnded. A restart starts the node on it, as silenceOf
// reads SILENT back. lowered keeps, per node, each lowering with the
// truncation the same Ready carried. A snapshot a Ready asks to send is
// installed as the driver does (installs counts them).
type pointNet struct {
	*silenceNet
	point    map[core.NodeID]uint64
	lowered  map[core.NodeID][]loweredAt
	installs int
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
		for _, to := range rd.SendSnapshot {
			h.sendSnapshot(r, to)
		}
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

// sendSnapshot ships r's base to peer to as the driver and the simulator
// do: the peer checks the offer on its loop and installs it, and r is told
// the transfer ended.
func (h *pointNet) sendSnapshot(r *Raft, to core.NodeID) {
	p := h.nodes[to]
	if p == nil || h.down[to] || r.role != core.RoleLeader {
		r.SnapshotDone(to, 0, false)
		return
	}
	idx := r.log.snapIndex
	eng := r.EngineAt(idx)
	meta := core.SnapshotMetaValue{
		ID: fmt.Sprintf("s-%d-%d", r.id, idx), Position: core.PositionValue{Term: r.log.snapTerm, Index: idx},
		ChainTip: r.log.snapChain, Membership: r.ConfAt(idx), Timing: eng.Timing,
		ClusterVersion: eng.Cluster, ClusterGeneration: eng.ClusterG, NoSpace: eng.NoSpace,
	}
	if code := p.CheckOffer(r.id, core.InstallOfferValue{Term: r.term, SnapshotID: meta.ID, Meta: meta}, p.NoTable()); code != 0 {
		r.SnapshotDone(to, 0, false)
		return
	}
	p.InstallSnapshot(meta)
	h.installs++
	r.SnapshotDone(to, idx, true)
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

// installedOverTheTail runs olderTailNet with C down while A is elected
// at term 2 and its no-op at 4 committed, then compacts A's log to 4, as a
// restart on its newest snapshot or a cut leaves it, and brings C back:
// A no longer holds the term of index 3, and sends C its snapshot (4, 2).
func installedOverTheTail(t *testing.T) *pointNet {
	t.Helper()
	h := olderTailNet(t, 3)
	a := h.nodes[1]
	h.now = t0.Add(3 * time.Second)
	a.Tick(h.now)
	h.settle()
	for range 10 {
		h.tick(100 * time.Millisecond)
	}
	if a.role != core.RoleLeader || a.term != 2 || a.termStart != 4 || a.log.committed != 4 {
		t.Fatalf("setup: node 1 role %v term %d, term start %d, commit %d", a.role, a.term, a.termStart, a.log.committed)
	}
	a.Compact(4)
	h.settle()
	if a.log.snapIndex != 4 {
		t.Fatalf("setup: A's log starts after %d, want 4", a.log.snapIndex)
	}
	h.down[3] = false
	return h
}

// #792: #791 lowered a silent node's truncation point only on the append
// path. Brought onto the leader's snapshot instead, over its tail 4..6 of
// term 1, C dropped that tail in InstallSnapshot (restartLogAt) and kept
// its point at 7, past A's whole log; A's one no-op, at 5, committed below
// it, and C stayed silent for as long as nobody wrote: once A failed, B and
// C elected no one. C held the snapshot's index under another term, so
// none of its entries from there on ever counted: the install lowers the
// point to 5, in the Ready that drops the tail, and A's no-op ends the
// silence.
func TestASilentNodeInstalledOverAnOlderTermsTailEndsItsSilenceInAQuietCluster(t *testing.T) {
	h := installedOverTheTail(t)
	a, c := h.nodes[1], h.nodes[3]
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if h.installs == 0 {
		t.Fatalf("setup: A never sent C a snapshot")
	}
	if c.cfg.SilentIndex != 0 || h.point[3] != 0 {
		t.Fatalf("installed over its tail, C is still silent %d/%d (persisted %d) after 30 s of a quiet cluster: A last %d commit %d, C last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3], a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	if got := h.lowered[3]; len(got) != 1 || got[0].index != 5 || got[0].truncateFrom == 0 {
		t.Fatalf("the Readies lowered C's point as %+v, want once to 5, in the Ready that drops the tail", got)
	}
	for i := uint64(1); i <= a.log.committed; i++ {
		want, _ := a.log.term(i)
		if got, ok := c.log.term(i); i > c.log.snapIndex && (!ok || got != want) {
			t.Fatalf("C's silence ended with index %d at term %d, A's is %d", i, got, want)
		}
	}
	if l := h.electedWithoutA(); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v", h.nodes[2].role, c.role)
	}
}

// #792 through #793's path: the point the install lowered is the one C's
// driver persists, so C restarted before A's no-op is committed to it
// comes back on 5, not 7, and ends its silence in a quiet cluster.
func TestAnInstalledSilencePointSurvivesARestart(t *testing.T) {
	h := installedOverTheTail(t)
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp && m.Commit >= 5 }
	a := h.nodes[1]
	for range 10 {
		h.tick(100 * time.Millisecond)
	}
	c := h.nodes[3]
	if h.installs == 0 || c.cfg.SilentIndex != 5 || h.point[3] != 5 || c.log.committed >= 5 {
		t.Fatalf("setup: installs %d, C silent %d/%d persisted %d commit %d; want the install, the point lowered to 5 and persisted, commit below 5",
			h.installs, c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3], c.log.committed)
	}
	h.drop = nil
	c = h.restartC()
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 {
		t.Fatalf("restarted after the install, C is still silent %d/%d after 30 s of a quiet cluster: A last %d commit %d, C last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
}

// An install lowers the point only when the log held the snapshot's index
// under another term, past the snapshot and below the point: a matching
// boundary keeps the suffix for the next append to judge, and a log that
// ends before the index proves nothing of the entries it lacks. The proof
// is the snapshot's commit, so it holds on a node whose term is below its
// SilentTerm too, one whose truncation dropped the record of a later term
// (#418), installed by a leader of its own term.
func TestAnInstallLowersTheSilencePointOnlyOverAnotherTerm(t *testing.T) {
	cases := []struct {
		name               string
		held               int
		silentTerm, silent uint64
		snapTerm, snapAt   uint64
		want               uint64
	}{
		{name: "the index held under another term", held: 6, silentTerm: 2, silent: 7, snapTerm: 2, snapAt: 4, want: 5},
		{name: "the index held at its term", held: 6, silentTerm: 2, silent: 7, snapTerm: 1, snapAt: 4, want: 7},
		{name: "a log that ends before the index", held: 3, silentTerm: 2, silent: 9, snapTerm: 2, snapAt: 5, want: 9},
		{name: "a point already past the snapshot", held: 6, silentTerm: 2, silent: 5, snapTerm: 2, snapAt: 4, want: 5},
		{name: "a node below its SilentTerm", held: 6, silentTerm: 3, silent: 7, snapTerm: 2, snapAt: 4, want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := quietSilenceCfg(3)
			cfg.SilentTerm, cfg.SilentIndex = tc.silentTerm, tc.silent
			r := NewRaft(cfg, &StartState{
				HardState: core.HardState{Term: 2, Commit: 2}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
				Entries: chainEntries(0, 1, 1, 1, 1, 1, 1, 1)[:tc.held], Now: t0,
			})
			r.Ready()
			r.InstallSnapshot(core.SnapshotMetaValue{Position: core.PositionValue{Term: tc.snapTerm, Index: tc.snapAt}, Membership: testConf(1, 2, 3)})
			if r.log.snapIndex != tc.snapAt {
				t.Fatalf("setup: the install left the base at %d, want %d", r.log.snapIndex, tc.snapAt)
			}
			rd := r.Ready()
			if r.cfg.SilentIndex != tc.want {
				t.Fatalf("SilentIndex %d after the install, want %d", r.cfg.SilentIndex, tc.want)
			}
			if lowered := tc.want != tc.silent; (rd.SilenceLowered != 0) != lowered || (lowered && rd.SilenceLowered != tc.want) {
				t.Fatalf("the Ready lowers the point to %d, want %d (lowered %v)", rd.SilenceLowered, tc.want, lowered)
			}
		})
	}
}
