package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// fedThenInstalled runs #787's quiet cluster (silentOverTheWholeLog: C,
// node 3, restarted SILENT(silentTerm, 12) holding A's whole log 1..11) up
// to the moment C has installed A's snapshot at 12:
//
//  1. C's heartbeat answer reports commit 11, A's last index: A feeds its
//     one no-op, at 12, which A and B commit. The append carrying it to C
//     is lost, C going down right after its answer.
//  2. While C is down, A's log is compacted through 12, a cut.
//  3. C comes back from the same disk, 1..11 and SILENT(silentTerm, 12).
//     A no longer holds term(11) and sends its snapshot at 12, which C
//     installs: its log ended at 11, so nothing lowers its point. Every
//     append to C is held back meanwhile: the window before A's next
//     append, the empty probe at 12, is where a restart of C falls.
func fedThenInstalled(t *testing.T, silentTerm uint64) *pointNet {
	t.Helper()
	h := &pointNet{
		silenceNet: silentOverTheWholeLog(t, silentTerm),
		point:      map[core.NodeID]uint64{3: 12}, lowered: map[core.NodeID][]loweredAt{},
	}
	a, c := h.nodes[1], h.nodes[3]
	disk := entriesOf(c)
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp && len(m.Entries) > 0 }
	for i := 0; i < 50 && a.log.lastIndex() < 12; i++ {
		h.tick(100 * time.Millisecond)
	}
	h.tick(100 * time.Millisecond)
	if a.log.lastIndex() != 12 || a.log.committed != 12 || c.log.lastIndex() != 11 || c.cfg.SilentIndex != 12 {
		t.Fatalf("setup: A last %d commit %d; C last %d silent %d/%d; want A's no-op at 12 committed, never reaching C",
			a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
	h.down[3] = true
	h.tick(100 * time.Millisecond)
	a.Compact(12)
	h.tick(100 * time.Millisecond)
	if a.log.snapIndex != 12 {
		t.Fatalf("setup: A's log starts after %d, want 12", a.log.snapIndex)
	}
	cfg := quietSilenceCfg(3)
	cfg.SilentTerm, cfg.SilentIndex = silentTerm, 12
	c = NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: c.term, Vote: c.vote, Commit: 11}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: disk, Now: h.now,
	})
	c.Ready()
	h.nodes[3] = c
	h.down[3] = false
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp }
	for i := 0; i < 50 && h.installs == 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if h.installs == 0 || c.log.snapIndex != 12 || c.log.committed != 12 || c.cfg.SilentIndex != 12 || h.point[3] != 12 {
		t.Fatalf("setup: installs %d; C snap %d commit %d silent %d/%d persisted %d; want A's snapshot at 12 installed, the point kept at 12",
			h.installs, c.log.snapIndex, c.log.committed, c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3])
	}
	if a.log.lastIndex() != 12 {
		t.Fatalf("setup: A appended up to %d before C's next append, want nothing past its no-op at 12", a.log.lastIndex())
	}
	h.drop = nil
	return h
}

// restartOnItsBase restarts node 3 on the snapshot its log starts after,
// the entries past it, its hard state and the silence condition its driver
// persisted, as its driver loads them.
func (h *pointNet) restartOnItsBase() *Raft {
	c := h.nodes[3]
	idx := c.log.snapIndex
	eng := c.EngineAt(idx)
	meta := core.SnapshotMetaValue{
		ID: fmt.Sprintf("s-3-%d", idx), Position: core.PositionValue{Term: c.log.snapTerm, Index: idx},
		ChainTip: c.log.snapChain, Membership: c.ConfAt(idx), Timing: eng.Timing,
		ClusterVersion: eng.Cluster, ClusterGeneration: eng.ClusterG, NoSpace: eng.NoSpace,
	}
	cfg := quietSilenceCfg(3)
	if p := h.point[3]; p != 0 {
		cfg.SilentTerm, cfg.SilentIndex = c.cfg.SilentTerm, p
	}
	c = NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: c.term, Vote: c.vote, Commit: c.log.committed}, Snapshot: meta,
		Entries: entriesOf(c), Now: h.now,
	})
	c.Ready()
	h.nodes[3] = c
	return c
}

// #795: C restarts, a crash or a clean stop, between installing A's
// snapshot at 12 and A's next append. On disk it holds that snapshot and
// SILENT(2, 12), and the start raises its point to 13 over the snapshot
// (raiseSilenceOverSnapshot, #636), past A's whole log. A had spent its one
// no-op for the silence (#787): in a cluster that wrote nothing C stayed
// silent, and once A failed B and C elected no one. A transfer that covers
// the no-op fed for a silence feeds the peer once more: A's no-op at 13
// ends the silence.
func TestASilentNodeRestartedOnAnInstalledSnapshotEndsItsSilenceInAQuietCluster(t *testing.T) {
	h := fedThenInstalled(t, 2)
	a := h.nodes[1]
	c := h.restartOnItsBase()
	if c.log.snapIndex != 12 || c.cfg.SilentIndex != 13 {
		t.Fatalf("setup: C restarted on snapshot %d silent %d/%d, want the point raised to 13 over the snapshot at 12",
			c.log.snapIndex, c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 || h.point[3] != 0 {
		t.Fatalf("restarted on A's snapshot, C is still silent %d/%d (persisted %d) after 30 s of a quiet cluster: A last %d commit %d, C last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3], a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	if e := a.log.at(13); a.log.lastIndex() != 13 || e.Kind != core.EntryNoOp || e.Position.Term != 2 {
		t.Fatalf("the silence ended with A last %d, want one more no-op of term 2, at 13", a.log.lastIndex())
	}
	if l := h.electedWithoutA(); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v silent %d/%d",
			h.nodes[2].role, c.role, c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
}

// Control: without the restart A's next append, the empty probe at 12,
// meets the condition at C's point 12, and the silence ends.
func TestASilentNodeInstalledOverTheNoOpEndsItsSilenceWithoutARestart(t *testing.T) {
	h := fedThenInstalled(t, 2)
	c := h.nodes[3]
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 || h.point[3] != 0 {
		t.Fatalf("C is still silent %d/%d (persisted %d) after 30 s of a quiet cluster", c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3])
	}
	if l := h.electedWithoutA(); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v", h.nodes[2].role, c.role)
	}
}

// The feed stays bounded: a silence no term ends, an unreadable condition,
// gets one more no-op for the transfer that covered the first, and no
// other through 30 s of a quiet cluster, each one committed and held by
// the peer.
func TestATransferFeedsASilenceNoTermEndsOnceMore(t *testing.T) {
	h := fedThenInstalled(t, 1<<62)
	a, c := h.nodes[1], h.nodes[3]
	for range 300 {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex == 0 || a.log.lastIndex() != 13 || a.log.committed != 13 || c.log.committed != 13 {
		t.Fatalf("over 30 s: C silent %v commit %d, A last %d commit %d; want C silent, and one more no-op, at 13, committed and held by C",
			c.cfg.SilentIndex != 0, c.log.committed, a.log.lastIndex(), a.log.committed)
	}
}

// Only a transfer that ended with the peer's install, at or past the no-op
// fed, feeds it again: a snapshot below that no-op leaves the point at most
// at it, and a failed transfer installed nothing.
func TestOnlyATransferOverTheNoOpFeedsTheSilentPeerAgain(t *testing.T) {
	cases := []struct {
		name  string
		index uint64
		ok    bool
		want  uint64
	}{
		{name: "at the no-op", index: 12, ok: true, want: 0},
		{name: "below the no-op", index: 11, ok: true, want: 12},
		{name: "a failed transfer", index: 12, want: 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := silentOverTheWholeLog(t, 2)
			a := h.nodes[1]
			h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp && len(m.Entries) > 0 }
			for i := 0; i < 50 && a.prs[3].silentFed == 0; i++ {
				h.tick(100 * time.Millisecond)
			}
			pr := a.prs[3]
			if pr.silentFed != 12 {
				t.Fatalf("setup: A fed C at %d, want its no-op at 12", pr.silentFed)
			}
			pr.becomeSnapshot(tc.index)
			a.SnapshotDone(3, tc.index, tc.ok)
			if pr.silentFed != tc.want {
				t.Fatalf("after a transfer at %d (ok %v) the no-op fed is %d, want %d", tc.index, tc.ok, pr.silentFed, tc.want)
			}
		})
	}
}
