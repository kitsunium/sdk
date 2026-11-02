package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// cutNet is pointNet whose node 3 stops at the first Ready that hands its
// apply the entry at, or past it: its driver cuts the snapshot at that
// Ready's applied index, and nothing else of the Ready reaches its disk or
// the network (cut, and ended when that Ready said SilenceEnded). seen,
// when set, sees every frame sent.
type cutNet struct {
	*pointNet
	at    uint64
	cut   bool
	ended bool
	seen  func(m core.MessageValue)
}

// round is pointNet.round, with node 3 stopped at its cut.
func (h *cutNet) round() int {
	var out []core.MessageValue
	for _, id := range []core.NodeID{1, 2, 3} {
		r := h.nodes[id]
		if r == nil || h.down[id] || !r.HasReady() {
			continue
		}
		rd := r.Ready()
		if id == 3 && h.at != 0 && r.log.applied >= h.at {
			h.cut, h.ended, h.down[3] = true, rd.SilenceEnded, true
			continue
		}
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
		if h.seen != nil {
			h.seen(m)
		}
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

func (h *cutNet) settle() {
	for range 200 {
		if h.round() == 0 && h.round() == 0 {
			return
		}
	}
	h.t.Fatal("the network never settled")
}

func (h *cutNet) tick(d time.Duration) {
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
func (h *cutNet) electedWithoutA() *Raft {
	h.down[1] = true
	for range 300 {
		h.tick(100 * time.Millisecond)
		if l := h.leaderAmong(2, 3); l != nil {
			return l
		}
	}
	return nil
}

// restartOnItsCut restarts node 3, speaking speaks (0: ProtocolMax), on
// the snapshot its driver cut at its applied index, no entry past it, and
// SILENT(silentTerm, the point its driver persisted).
func (h *cutNet) restartOnItsCut(silentTerm uint64, speaks uint32) *Raft {
	c := h.nodes[3]
	idx := c.log.applied
	term, _ := c.log.term(idx)
	eng := c.EngineAt(idx)
	meta := core.SnapshotMetaValue{
		ID: fmt.Sprintf("s-3-%d", idx), Position: core.PositionValue{Term: term, Index: idx},
		ChainTip: c.log.chain(idx), Membership: c.ConfAt(idx), Timing: eng.Timing,
		ClusterVersion: eng.Cluster, ClusterGeneration: eng.ClusterG, NoSpace: eng.NoSpace,
	}
	cfg := quietSilenceCfg(3)
	cfg.SilentTerm, cfg.SilentIndex, cfg.speaks = silentTerm, h.point[3], speaks
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: c.term, Vote: c.vote, Commit: idx}, Snapshot: meta, Now: h.now,
	})
	r.Ready()
	h.nodes[3], h.down[3] = r, false
	return r
}

// cutOverTheNoOp runs #787's quiet cluster (silentOverTheWholeLog: C, node
// 3, restarted SILENT(silentTerm, 12) holding A's whole log 1..11), A
// speaking aSpeaks and C cSpeaks (0: ProtocolMax), up to the window of
// #795's pass-80 comment, and restarts C in it:
//
//  1. C's heartbeat answer reports commit 11, A's last index: A feeds its
//     one no-op, at 12, which A and B commit, and C holds it, committed and
//     durable. Under a leader of a term at least its own, that meets C's
//     condition.
//  2. The Ready that hands C's apply the no-op is the one whose
//     SilenceEnded has the driver remove SILENT. C's driver cuts C's
//     snapshot at 12 and C stops before anything else of that Ready
//     reaches its disk: on disk, its own cut over the no-op and
//     SILENT(silentTerm, 12).
//  3. C restarts on that cut. The start raises its point to 13 over it
//     (raiseSilenceOverSnapshot, #636), past A's whole log, and A saw no
//     transfer that feeds it again (SnapshotDone, #795's first fix).
func cutOverTheNoOp(t *testing.T, silentTerm uint64, aSpeaks, cSpeaks uint32) *cutNet {
	t.Helper()
	h := &cutNet{pointNet: &pointNet{
		silenceNet: silentOverTheWholeLog(t, silentTerm),
		point:      map[core.NodeID]uint64{3: 12}, lowered: map[core.NodeID][]loweredAt{},
	}, at: 12}
	a := h.nodes[1]
	a.cfg.speaks, h.nodes[3].cfg.speaks = aSpeaks, cSpeaks
	for i := 0; i < 50 && !h.cut; i++ {
		h.tick(100 * time.Millisecond)
	}
	own := silentTerm <= a.term
	if !h.cut || h.ended != own || h.point[3] != 12 || a.log.lastIndex() != 12 || a.log.committed != 12 {
		t.Fatalf("setup: C cut %v (silence ended %v) persisted %d; A last %d commit %d; want C's cut over A's no-op at 12, committed, SILENT still 12",
			h.cut, h.ended, h.point[3], a.log.lastIndex(), a.log.committed)
	}
	h.at = 0
	c := h.restartOnItsCut(silentTerm, cSpeaks)
	if c.log.snapIndex != 12 || c.cfg.SilentIndex != 13 {
		t.Fatalf("setup: C restarted on snapshot %d silent %d/%d, want the point raised to 13 over its cut at 12",
			c.log.snapIndex, c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
	return h
}

// #795, the pass-80 variant: C cut its own snapshot over A's no-op once its
// condition held, before the end of its silence reached its disk, and
// restarted on it, its point raised to 13, past A's whole log. A had spent
// its one no-op, and saw no transfer: in a cluster that wrote nothing C
// stayed silent, and once A failed B and C elected no one. C's heartbeat
// answer carries its point, past A's last index, and A feeds it once more:
// its no-op at 13 ends the silence.
func TestASilentNodeRestartedOnItsOwnCutOverTheNoOpEndsItsSilenceInAQuietCluster(t *testing.T) {
	h := cutOverTheNoOp(t, 2, 0, 0)
	a, c := h.nodes[1], h.nodes[3]
	for i := 0; i < 300 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 || h.point[3] != 0 {
		t.Fatalf("restarted on its own cut over the no-op, C is still silent %d/%d (persisted %d) after 30 s of a quiet cluster: A last %d commit %d, C last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3], a.log.lastIndex(), a.log.committed, c.log.lastIndex(), c.log.committed)
	}
	if a.log.lastIndex() != 13 {
		t.Fatalf("the silence ended with A last %d, want one more no-op of term 2, at 13", a.log.lastIndex())
	}
	if e := a.log.at(13); e.Kind != core.EntryNoOp || e.Position.Term != 2 {
		t.Fatalf("entry 13 is kind %v of term %d, want a no-op of term 2", e.Kind, e.Position.Term)
	}
	if l := h.electedWithoutA(); l == nil {
		t.Fatalf("with A down, B and C elected no one: B role %v, C role %v silent %d/%d",
			h.nodes[2].role, c.role, c.cfg.SilentTerm, c.cfg.SilentIndex)
	}
}

// silentAnswer is a heartbeat answer of leader r's term from node 3, silent
// (FlagTruncated) with flags beside, reporting its log durable and
// committed through commit and the truncation point point.
func silentAnswer(r *Raft, flags core.MessageFlags, commit, point uint64) core.MessageValue {
	m := msg(core.MsgHeartbeatResp, 3, 1, r.term)
	m.Flags = core.FlagTruncated | flags
	m.RejectHint, m.Commit, m.LogTerm = commit, commit, point
	return m
}

// fedOnce is #787's quiet cluster once A fed C its one no-op, at 12, and
// committed it, the appends to C lost: A's last index is 12, and C reports
// nothing yet.
func fedOnce(t *testing.T) *Raft {
	t.Helper()
	h := silentOverTheWholeLog(t, 2)
	a := h.nodes[1]
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp && len(m.Entries) > 0 }
	for i := 0; i < 50 && (a.prs[3].silentFed == 0 || a.log.committed < 12); i++ {
		h.tick(100 * time.Millisecond)
	}
	if a.prs[3].silentFed != 12 || a.log.lastIndex() != 12 || a.log.committed != 12 {
		t.Fatalf("setup: A fed C at %d, last %d commit %d; want its no-op at 12, committed", a.prs[3].silentFed, a.log.lastIndex(), a.log.committed)
	}
	return a
}

// The feed stays bounded: a leader feeds a silent peer again only for a
// point it reports past the leader's last index, once per point, and only
// while the peer reports the whole log committed. A point at or below the
// last index, the point it was fed for, or a peer behind the log, gets
// nothing.
func TestTheLeaderFeedsASilentPeerOncePerPointItReportsPastTheLog(t *testing.T) {
	a := fedOnce(t)
	steps := []struct {
		name          string
		commit, point uint64
		want          uint64
	}{
		{name: "a point at the last index", commit: 12, point: 12, want: 12},
		{name: "a point past the last index", commit: 12, point: 14, want: 13},
		{name: "the point it was fed for, still past", commit: 13, point: 14, want: 13},
		{name: "a new point, the peer behind the log", commit: 12, point: 15, want: 13},
		{name: "a new point past the log", commit: 13, point: 15, want: 14},
		{name: "the point it was fed for, the whole log held", commit: 14, point: 15, want: 14},
		{name: "a point the log reached", commit: 14, point: 14, want: 14},
	}
	for _, s := range steps {
		a.Step(a.now, silentAnswer(a, announcementOf(ProtocolMax), s.commit, s.point))
		a.Ready()
		if a.log.lastIndex() != s.want {
			t.Fatalf("%s (commit %d, point %d): A last %d, want %d", s.name, s.commit, s.point, a.log.lastIndex(), s.want)
		}
	}
	if e := a.log.at(14); e.Kind != core.EntryNoOp || e.Position.Term != a.term {
		t.Fatalf("entry 14 is kind %v of term %d, want a no-op of A's term", e.Kind, e.Position.Term)
	}
}

// An unreadable condition, which no term ends, reports no point: no leader
// it hears can end its silence. Restarted on its own cut over the no-op, it
// gets nothing more than its one no-op through 30 s of a quiet cluster, as
// before (#787).
func TestAnUnreadableConditionRestartedOnItsCutIsFedNoMore(t *testing.T) {
	h := cutOverTheNoOp(t, 1<<62, 0, 0)
	a, c := h.nodes[1], h.nodes[3]
	var points []uint64
	h.seen = func(m core.MessageValue) {
		if m.From == 3 && m.Kind == core.MsgHeartbeatResp && m.LogTerm != 0 {
			points = append(points, m.LogTerm)
		}
	}
	for range 300 {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex == 0 || a.log.lastIndex() != 12 || len(points) != 0 {
		t.Fatalf("over 30 s: C silent %v, A last %d, %d answers of C carried a point; want C silent, nothing past A's no-op at 12, no point",
			c.cfg.SilentIndex != 0, a.log.lastIndex(), len(points))
	}
}

// answersOf records, on h, every heartbeat answer node 3 sends.
func answersOf(h *cutNet) *[]core.MessageValue {
	var out []core.MessageValue
	h.seen = func(m core.MessageValue) {
		if m.From == 3 && m.Kind == core.MsgHeartbeatResp {
			out = append(out, m)
		}
	}
	return &out
}

// stillSilentThenWritten checks that C, node 3, is still silent at 2/13
// after 30 s of a quiet cluster with A's log ending at its one no-op, the
// residual ADR 0152 states for a pair that does not both speak protocol 4,
// and that one write at A, the operator's way out, ends the silence.
func stillSilentThenWritten(t *testing.T, h *cutNet) {
	t.Helper()
	a, c := h.nodes[1], h.nodes[3]
	for range 300 {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 13 || a.log.lastIndex() != 12 {
		t.Fatalf("over 30 s: C silent %d/%d, A last %d; want today's residual, C at 13 and nothing past A's no-op at 12",
			c.cfg.SilentTerm, c.cfg.SilentIndex, a.log.lastIndex())
	}
	if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(13)}); code != 0 {
		t.Fatalf("propose: %v", code)
	}
	for i := 0; i < 50 && c.cfg.SilentIndex != 0; i++ {
		h.tick(100 * time.Millisecond)
	}
	if c.cfg.SilentIndex != 0 || h.point[3] != 0 {
		t.Fatalf("one write did not end C's silence: C silent %d/%d (persisted %d), A last %d commit %d",
			c.cfg.SilentTerm, c.cfg.SilentIndex, h.point[3], a.log.lastIndex(), a.log.committed)
	}
}

// A mixed cluster, a leader of protocol 4 and a silent peer of protocol 3:
// the peer neither reads the announcement nor reports its point, and the
// leader keeps today's feed, one no-op per silence and per covering
// transfer. Restarted on its own cut over the no-op, the peer stays silent
// until a write, as before (ADR 0152's residual, #795).
func TestASilentPeerOfProtocol3ReportsNoPointToALeaderOfProtocol4(t *testing.T) {
	h := cutOverTheNoOp(t, 2, 0, Protocol3)
	answers := answersOf(h)
	stillSilentThenWritten(t, h)
	if len(*answers) == 0 {
		t.Fatal("C sent no heartbeat answer")
	}
	for _, m := range *answers {
		if m.Flags&core.FlagProtocol4 != 0 || m.LogTerm != 0 {
			t.Fatalf("a peer of protocol 3 answered with flags %#x and LogTerm %d, want no announcement of 4 and no point", m.Flags, m.LogTerm)
		}
	}
}

// The other mix, a leader of protocol 3 and a silent peer of protocol 4:
// the leader's heartbeats do not announce 4, so the peer writes no point,
// which that leader would not read, and the leader keeps today's feed.
func TestALeaderOfProtocol3IsReportedNoPoint(t *testing.T) {
	h := cutOverTheNoOp(t, 2, Protocol3, 0)
	answers := answersOf(h)
	stillSilentThenWritten(t, h)
	silent := 0
	for _, m := range *answers {
		if m.To != 1 || m.Flags&core.FlagTruncated == 0 {
			continue
		}
		silent++
		if m.Flags&core.FlagProtocol4 == 0 || m.LogTerm != 0 {
			t.Fatalf("a peer of protocol 4 answered a leader of protocol 3 with flags %#x and LogTerm %d, want the announcement and no point", m.Flags, m.LogTerm)
		}
	}
	if silent == 0 {
		t.Fatal("C sent A no silent heartbeat answer")
	}
}

// A leader of protocol 3 reads no point, even from an answer that carries
// one: the slot means nothing to it.
func TestALeaderOfProtocol3ReadsNoPoint(t *testing.T) {
	a := fedOnce(t)
	a.cfg.speaks = Protocol3
	a.Step(a.now, silentAnswer(a, announcementOf(Protocol4), 12, 14))
	a.Ready()
	if a.log.lastIndex() != 12 {
		t.Fatalf("a leader of protocol 3 fed a point it does not read: A last %d, want 12", a.log.lastIndex())
	}
	a.cfg.speaks = 0
	a.Step(a.now, silentAnswer(a, announcementOf(Protocol3), 12, 14))
	a.Ready()
	if a.log.lastIndex() != 12 {
		t.Fatalf("a leader of protocol 4 read a point from an answer of protocol 3: A last %d, want 12", a.log.lastIndex())
	}
}

// A node of protocol 4 announces protocols 2, 3 and 4 on every message, one
// of protocol 3 announces 2 and 3 only (D18).
func TestEveryMessageAnnouncesProtocol4(t *testing.T) {
	all := core.FlagProtocol2 | core.FlagProtocol3 | core.FlagProtocol4
	for _, tc := range []struct {
		speaks uint32
		want   core.MessageFlags
	}{{0, all}, {Protocol3, core.FlagProtocol2 | core.FlagProtocol3}} {
		f := newTest(2, testState{term: 1}, 0, false)
		f.cfg.speaks = tc.speaks
		f.Step(t0, msg(core.MsgHeartbeat, 1, 2, 1))
		rd := f.Ready()
		answers := append(rd.Messages, rd.AfterSync...)
		if len(answers) == 0 {
			t.Fatalf("setup: the node speaking %d answered nothing", tc.speaks)
		}
		for _, m := range answers {
			if got := m.Flags & all; got != tc.want {
				t.Fatalf("a node speaking %d announced %#x on %v, want %#x", tc.speaks, got, m.Kind, tc.want)
			}
		}
	}
}

// The cluster version is raised to 4 only once every member was heard in
// this term announcing protocol 4, and a member rolled back to protocol 3
// withdraws that announcement at its next message (D18).
func TestARaiseTo4WaitsForEveryMemberToAnnounceIt(t *testing.T) {
	answer := func(r *Raft, id core.NodeID, v uint32) core.MessageValue {
		m := msg(core.MsgHeartbeatResp, id, 1, r.term)
		m.Flags = announcementOf(v)
		return m
	}
	r := ackedLeader(0)
	r.Step(r.now, answer(r, 2, Protocol4))
	r.Step(r.now, answer(r, 3, Protocol3))
	if code := proposeRaise(r, Protocol4); code != codeVersion {
		t.Fatalf("the raise to 4 while member 3 announces protocol 3 only: %v, want VERSION_UNSUPPORTED", code)
	}
	r.Step(r.now, answer(r, 3, Protocol4))
	r.Step(r.now, answer(r, 3, Protocol3))
	if code := proposeRaise(r, Protocol4); code != codeVersion {
		t.Fatalf("the raise to 4 while member 3's last message announced protocol 3 only: %v, want VERSION_UNSUPPORTED", code)
	}
	r.Step(r.now, answer(r, 3, Protocol4))
	if code := proposeRaise(r, Protocol4); code != 0 {
		t.Fatalf("the raise to 4 every member announced: %v", code)
	}
}
