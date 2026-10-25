package raft

import (
	"errors"
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// silenceNet is a three-node network of step functions: every write is made
// durable at once, AfterSync leaves after it, and a filter drops frames.
type silenceNet struct {
	t     *testing.T
	nodes map[core.NodeID]*Raft
	down  map[core.NodeID]bool
	drop  func(m core.MessageValue) bool
	// stepped, when set, runs after every frame delivered.
	stepped func(m core.MessageValue)
	now     time.Time
}

// round runs one turn on every live node and delivers what it sent; it
// returns how many frames were delivered.
func (h *silenceNet) round() int {
	var out []core.MessageValue
	for _, id := range []core.NodeID{1, 2, 3} {
		r := h.nodes[id]
		if r == nil || h.down[id] || !r.HasReady() {
			continue
		}
		rd := r.Ready()
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
			if h.stepped != nil {
				h.stepped(m)
			}
		}
	}
	return n
}

func (h *silenceNet) settle() {
	for range 200 {
		if h.round() == 0 && h.round() == 0 {
			return
		}
	}
	h.t.Fatal("the network never settled")
}

// tick moves the clock by d, ticks every live node and settles.
func (h *silenceNet) tick(d time.Duration) {
	h.now = h.now.Add(d)
	for _, id := range []core.NodeID{1, 2, 3} {
		if r := h.nodes[id]; r != nil && !h.down[id] {
			r.Tick(h.now)
		}
	}
	h.settle()
}

func silenceCmd(i int) core.EntryValue {
	d := fmt.Appendf(nil, "w%02d", i)
	return core.EntryValue{Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}
}

// silencedUnderItsLeader is the scenario of #636 up to the victim's restart.
// Node 1 (A) leads term 2. Node 3 (C) is slow: its heartbeat answers keep
// A's quorum contact, but no append reaches it. A commits entries 4 to 11
// with node 2's (B) acknowledgements. B's synced record at index 6 then
// rots: B is quarantined, the operator takes TruncateAndStaySilent, and B
// restarts with entries 1..5, the hard state its surviving records name,
// and SILENT(term 2, index 6), while A still leads term 2. A batch holds
// two entries.
func silencedUnderItsLeader(t *testing.T) *silenceNet {
	t.Helper()
	cfgOf := func(id core.NodeID) *Config {
		return &Config{
			ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
			MaxBatchBytes: 150, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
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
	h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp }
	for i := 4; i <= 11; i++ {
		if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(i)}); code != 0 {
			t.Fatalf("propose %d: %v", i, code)
		}
		h.settle()
	}
	if a.log.committed != 11 || h.nodes[2].log.lastIndex() != 11 || h.nodes[3].log.lastIndex() != 3 {
		t.Fatalf("setup: A commit %d, B last %d, C last %d", a.log.committed, h.nodes[2].log.lastIndex(), h.nodes[3].log.lastIndex())
	}
	bcfg := cfgOf(2)
	bcfg.SilentTerm, bcfg.SilentIndex = 2, 6
	kept := make([]core.EntryValue, 0, 5)
	for i := uint64(1); i <= 5; i++ {
		kept = append(kept, *a.log.at(i))
	}
	// B is down for 10 s (inspect, ClearQuarantine, restart); A keeps
	// leading on C's heartbeat answers.
	h.down[2] = true
	for range 100 {
		h.tick(100 * time.Millisecond)
	}
	if a.role != core.RoleLeader || a.term != 2 {
		t.Fatalf("setup: A lost its leadership while B was down (role %v term %d)", a.role, a.term)
	}
	b := NewRaft(bcfg, &StartState{
		HardState: core.HardState{Term: 2, Vote: 1, Commit: 3}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: kept, Now: h.now,
	})
	b.Ready()
	h.nodes[2] = b
	h.down[2] = false
	return h
}

// leaderAmong is the leader among the live nodes ids, nil when none leads.
func (h *silenceNet) leaderAmong(ids ...core.NodeID) *Raft {
	for _, id := range ids {
		if r := h.nodes[id]; r != nil && !h.down[id] && r.role == core.RoleLeader {
			return r
		}
	}
	return nil
}

// #636: under the leader of the node's own SilentTerm, committing an entry
// of that term past the truncation point proves nothing: that leader
// committed the entries the node lost with the node's own
// acknowledgements. The silence ended after ONE batch past the truncation
// point while B still lacked entries 8..11; A then failed, B voted again
// and B and C committed index 8 at term 3 over the entries A had committed
// at term 2.
func TestTheSilenceHoldsUntilTheSameTermLeadersCommit(t *testing.T) {
	h := silencedUnderItsLeader(t)
	a, b, c := h.nodes[1], h.nodes[2], h.nodes[3]
	// A new write: A's append is refused with FlagTruncated, A probes from
	// 6 and sends one batch (two entries).
	if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(12)}); code != 0 {
		t.Fatalf("propose 12: %v", code)
	}
	for range 20 {
		h.round()
		if b.cfg.SilentIndex == 0 {
			break
		}
	}
	t.Logf("after the probe batch: B last %d commit %d silent %v, A commit %d", b.log.lastIndex(), b.log.committed, b.cfg.SilentIndex != 0, a.log.committed)
	// A fails right then, before it sends B anything more; C is reachable
	// again.
	h.down[1] = true
	h.drop = nil
	for range 40 {
		h.tick(100 * time.Millisecond)
	}
	lead := h.leaderAmong(2, 3)
	if lead == nil {
		t.Logf("no leader among B and C (B role %v term %d silent %v, C role %v term %d): safe",
			b.role, b.term, b.cfg.SilentIndex != 0, c.role, c.term)
		return
	}
	at8A, _ := a.log.term(8)
	at8, _ := lead.log.term(8)
	if lead.log.committed >= 8 && at8 != at8A {
		t.Fatalf("committed entries lost: A committed 8..11 at term 2 with B's acknowledgements, and node %d commits index 8 at term %d",
			lead.id, at8)
	}
}

// #636, the liveness half: under that same leader, the silence ends once
// the node holds the leader's commit, and the node votes again. It needs
// several batches past the truncation point: the leader moves its probe on
// the commit a silent node's heartbeat answer reports, since the node
// acknowledges no append. B is then elected with C's vote once A fails, and
// every entry A committed is in its log.
func TestTheSilenceEndsOnceTheSameTermLeadersCommitIsHeld(t *testing.T) {
	h := silencedUnderItsLeader(t)
	a, b, c := h.nodes[1], h.nodes[2], h.nodes[3]
	ended := false
	h.stepped = func(m core.MessageValue) {
		if ended || m.To != 2 || b.cfg.SilentIndex != 0 {
			return
		}
		ended = true
		if b.log.committed < a.log.committed {
			t.Fatalf("the silence ended at commit %d below the leader's %d", b.log.committed, a.log.committed)
		}
	}
	if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(12)}); code != 0 {
		t.Fatalf("propose 12: %v", code)
	}
	h.settle()
	for i := 0; i < 50 && !ended; i++ {
		h.tick(100 * time.Millisecond)
	}
	if !ended {
		t.Fatalf("the silence never ended under the leader of SilentTerm: B last %d commit %d, A commit %d (B %s)",
			b.log.lastIndex(), b.log.committed, a.log.committed, b.Debug())
	}
	// The write B's silence held back commits with B's acknowledgement.
	h.tick(100 * time.Millisecond)
	if a.log.committed != 12 {
		t.Fatalf("A committed %d once B rejoined, want 12", a.log.committed)
	}
	h.stepped = nil
	h.down[1] = true
	h.drop = nil
	var lead *Raft
	for i := 0; i < 60 && lead == nil; i++ {
		h.tick(100 * time.Millisecond)
		lead = h.leaderAmong(2, 3)
	}
	if lead == nil {
		t.Fatalf("no leader among B and C once A failed: B role %v term %d silent %v, C role %v term %d",
			b.role, b.term, b.cfg.SilentIndex != 0, c.role, c.term)
	}
	if lead != b {
		t.Fatalf("node %d leads term %d, want B, the only one holding A's commit", lead.id, lead.term)
	}
	for i := uint64(4); i <= 12; i++ {
		want, _ := a.log.term(i)
		if got, ok := b.log.term(i); !ok || got != want {
			t.Fatalf("index %d is term %d on the new leader, term %d on A", i, got, want)
		}
	}
}

// #636, over a snapshot: the damage is in the entries the log trails behind
// its newest snapshot (SnapshotTrailing), the truncation drops every entry
// past it, the start restores the snapshot at 20 and moves the log onto it,
// and SilentIndex, the truncated log's end + 1, lies below the snapshot.
// The first append of the leader of SilentTerm, refused since the node holds
// nothing past 20, ended the silence: committed 20 was an entry of the
// leader's term past the truncation point.
func TestTheSilenceNeverEndsOnARefusedAppendOverASnapshot(t *testing.T) {
	cfg := &Config{
		ID: 2, Group: 1, Seed: 2, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		SilentTerm: 2, SilentIndex: 11,
	}
	snap := core.SnapshotMetaValue{Position: core.PositionValue{Term: 2, Index: 20}, Membership: testConf(1, 2, 3)}
	b := NewRaft(cfg, &StartState{HardState: core.HardState{Term: 2, Vote: 1, Commit: 20}, Snapshot: snap, Applied: 20, Now: t0})
	b.Ready()
	// The leader of term 2 last matched B at 40 (B acknowledged 21..40
	// before the damage) and appends 41 from there.
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Entries, m.Commit = 40, 2, chainEntries(0, 41, 2), 40
	b.Step(t0.Add(time.Second), m)
	rd := b.Ready()
	if rd.SilenceEnded || b.cfg.SilentIndex == 0 {
		t.Fatalf("the silence ended on a refused append: B holds nothing past its snapshot at 20 and acknowledged through 40")
	}
	if granted(rd, core.MsgAppResp) {
		t.Fatal("a silent node acknowledged an append")
	}
}

// silentAt is node 2 of members 1, 2, 3, left silent at (SilentTerm 2,
// SilentIndex silentIndex) over the snapshot snap, holding ents after it
// and committed through commit, at term 2.
func silentAt(mut mutation, silentIndex uint64, snap core.PositionValue, ents []core.EntryValue, commit uint64) *Raft {
	r := NewRaft(&Config{
		ID: 2, Group: 1, Seed: 2, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		SilentTerm: 2, SilentIndex: silentIndex, mut: mut,
	}, &StartState{
		HardState: core.HardState{Term: 2, Vote: 1, Commit: commit},
		Snapshot:  core.SnapshotMetaValue{Position: snap, Membership: testConf(1, 2, 3)},
		Entries:   ents, Applied: snap.Index, Now: t0,
	})
	r.Ready()
	return r
}

// stillSilent reports how the silence ended, nil when it holds: in r's
// Ready, or in the one after that Ready's write is durable, since a silence
// whose condition holds ends only once its entries are (#684).
func stillSilent(r *Raft) error {
	for range 2 {
		rd := r.Ready()
		if rd.SilenceEnded || r.cfg.SilentIndex == 0 {
			return errors.New("the silence ended")
		}
		if granted(rd, core.MsgAppResp) {
			return errors.New("a silent node acknowledged an append")
		}
		if rd.Seq != 0 {
			r.StorageDone(rd.Seq, r.log.lastIndex(), r.log.lastTerm())
		}
	}
	return nil
}

// checkSilenceHoldsTheLeadersCommit: under the leader of the node's own
// SilentTerm, the silence holds until the node holds that leader's commit,
// however far past the truncation point it committed an entry of the term
// (#636).
func checkSilenceHoldsTheLeadersCommit(mut mutation) error {
	ents := chainEntries(0, 1, 1, 1, 1, 2, 2, 2, 2)
	r := silentAt(mut, 6, core.PositionValue{}, ents[:5], 3)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Entries, m.Commit = 5, 2, ents[5:7], 11
	r.Step(t0.Add(time.Second), m)
	if err := stillSilent(r); err != nil {
		return fmt.Errorf("%w at commit %d, below the commit %d of the leader of SilentTerm", err, r.log.committed, m.Commit)
	}
	return nil
}

// checkSilenceNeverEndsOnARefusal: a refused append proves nothing about
// the node's log, even when its state already meets the condition: the
// leader believes the node holds more than it does (#636).
func checkSilenceNeverEndsOnARefusal(mut mutation) error {
	r := silentAt(mut, 4, core.PositionValue{}, chainEntries(0, 1, 2, 2, 2, 2, 2, 2), 6)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Commit = 8, 2, 6
	m.Entries = []core.EntryValue{{Position: core.PositionValue{Term: 2, Index: 9}, Kind: core.EntryCommand}}
	r.Step(t0.Add(time.Second), m)
	if err := stillSilent(r); err != nil {
		return fmt.Errorf("%w on a refused append", err)
	}
	return nil
}

// checkSilenceNotEndedByItsOwnSnapshot: a SilentIndex the start's own
// snapshot covers is raised past it, so the snapshot's commit, a past the
// node may have acknowledged beyond, never ends the silence (#636).
func checkSilenceNotEndedByItsOwnSnapshot(mut mutation) error {
	r := silentAt(mut, 11, core.PositionValue{Term: 2, Index: 20}, nil, 20)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Commit = 20, 2, 20
	r.Step(t0.Add(time.Second), m)
	if err := stillSilent(r); err != nil {
		return fmt.Errorf("%w on its own snapshot's commit", err)
	}
	return nil
}

// checkSilenceEndsOnceDurable: an accepted append that meets the condition
// ends the silence once the entries it brought are durable, never in the
// Ready that hands them to the driver, which ends the silence on disk at
// once: a crash during their sync restarted a voter whose log stopped at
// the truncation point (#684). It ends it then, acknowledging the commit.
func checkSilenceEndsOnceDurable(mut mutation) error {
	ents := chainEntries(0, 1, 1, 1, 1, 2, 2, 2, 2)
	r := silentAt(mut, 6, core.PositionValue{}, ents[:5], 3)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Entries, m.Commit = 5, 2, ents[5:], 7
	r.Step(t0.Add(time.Second), m)
	rd := r.Ready()
	if rd.SilenceEnded || r.cfg.SilentIndex == 0 || granted(rd, core.MsgAppResp) {
		return fmt.Errorf("the silence ended with entries 6..7, which meet its condition, durable through %d only", r.log.stable)
	}
	r.StorageDone(rd.Seq, 7, 2)
	rd = r.Ready()
	if !rd.SilenceEnded || r.cfg.SilentIndex != 0 || !granted(rd, core.MsgAppResp) {
		return fmt.Errorf("the silence held once entries 6..7, which meet its condition, were durable (ended %v, silent index %d)",
			rd.SilenceEnded, r.cfg.SilentIndex)
	}
	return nil
}

// checkSilentHeartbeatReports: a silent node's heartbeat answer carries
// FlagTruncated with the end of its durable log and its commit, and the
// leader lowers a match that still counts the dropped entries, then probes
// past the reported commit (#636).
func checkSilentHeartbeatReports(mut mutation) error {
	r := silentAt(mut, 6, core.PositionValue{}, chainEntries(0, 1, 1, 1, 1, 2, 2), 3)
	r.Step(t0.Add(time.Second), heartbeatOf(1, 2, 2, 5))
	var resp core.MessageValue
	for _, x := range r.Ready().Messages {
		if x.Kind == core.MsgHeartbeatResp {
			resp = x
		}
	}
	if resp.Flags&core.FlagTruncated == 0 || resp.RejectHint != 5 || resp.Commit != 5 {
		return fmt.Errorf("a silent node's heartbeat answer: flags %b hint %d commit %d, want FlagTruncated, 5, 5",
			resp.Flags, resp.RejectHint, resp.Commit)
	}
	l := leaderOf(2, 11, chainEntries(0, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2), mut, false)
	pr := l.prs[2]
	pr.match, pr.next = 11, 13
	pr.becomeReplicate()
	resp.Term = 2
	l.Step(l.now, resp)
	if pr.match != 5 || pr.state != stateProbe || pr.next != 6 {
		return fmt.Errorf("after a silent answer: match %d next %d state %v, want 5, 6, probe", pr.match, pr.next, pr.state)
	}
	resp.Commit = 9
	l.Step(l.now, resp)
	if pr.match != 5 || pr.next != 10 {
		return fmt.Errorf("after a silent answer at commit 9: match %d next %d, want 5, 10", pr.match, pr.next)
	}
	return nil
}

// heartbeatOf is a heartbeat of term from the leader from to to, carrying
// commit.
func heartbeatOf(from, to core.NodeID, term, commit uint64) core.MessageValue {
	m := msg(core.MsgHeartbeat, from, to, term)
	m.Commit = commit
	return m
}
