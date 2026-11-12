package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// foundingOf is the entry 1 a founder writes at Bootstrap over the members
// ids, its contract's election window starting at lo: founders given
// different timings write different entries at (1, 1) (#698).
func foundingOf(lo time.Duration, ids ...core.NodeID) core.EntryValue {
	c := core.TimingContractSpec{
		Generation: 1, HeartbeatInterval: lo / 10, ElectionTimeoutMin: lo, ElectionTimeoutMax: 2 * lo, MaxClockDrift: 0.01,
	}
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "bootstrap", Change: wire.ChangeFounding, Membership: testConf(ids...), Contract: c})
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 1}, Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}
	e.ChainSum = wire.ChainSum(0, &e)
	return e
}

// foundingLeaderLog is a leader's log of term 2 over its own entry 1.
func foundingLeaderLog(e1 core.EntryValue) []core.EntryValue {
	return append([]core.EntryValue{e1}, chainEntries(e1.ChainSum, 2, 2)...)
}

// acknowledged reports whether rd acknowledges the leader 1's append at
// index at.
func acknowledged(rd *ReadyValue, at uint64) bool {
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.To == 1 && m.Kind == core.MsgAppResp && !m.Reject && m.Index == at {
			return true
		}
	}
	return false
}

// TestAnUncommittedFoundingEntryGivesWayToTheLeaders: a founder whose entry
// 1, written from its own InitialTiming, differs from its leader's holds it
// uncommitted, and takes the leader's as any conflicting uncommitted
// entry: one refusal of the append past entry 1, below MaxChainRefusals,
// then the append from index 0 replaces entry 1, and once the leader's
// commit reaches the node it runs on the leader's contract. Matched on the
// term alone, entry 1 was kept, and every append was refused (#698).
func TestAnUncommittedFoundingEntryGivesWayToTheLeaders(t *testing.T) {
	own, theirs := foundingOf(100*time.Millisecond, 1, 2, 3), foundingOf(150*time.Millisecond, 1, 2, 3)
	leader := foundingLeaderLog(theirs)
	f := newTest(2, testState{term: 1, ents: []core.EntryValue{own}}, 0, false)
	at := t0.Add(5 * time.Second)
	past := msg(core.MsgApp, 1, 2, 2)
	past.Index, past.LogTerm, past.Entries, past.Commit = 1, 1, leader[1:], 2
	f.Step(at, past)
	f.Ready()
	if _, _, n := f.ChainRefusal(); n >= 3 {
		t.Fatalf("%d refusals: the leader is refused for the term", n)
	}
	whole := msg(core.MsgApp, 1, 2, 2)
	whole.Entries, whole.Commit = leader, 2
	f.Step(at.Add(time.Millisecond), whole)
	rd := f.Ready()
	if !acknowledged(&rd, 2) {
		t.Fatalf("the append from index 0 was not acknowledged: %+v", rd.Messages)
	}
	if f.log.chain(1) != theirs.ChainSum || f.log.chain(2) != leader[1].ChainSum {
		t.Fatal("the node does not hold the leader's entry 1")
	}
	if st := f.Status(); st.Commit != 2 || st.Timing.ElectionTimeoutMin != 150*time.Millisecond || st.Timing.Generation != 1 {
		t.Fatalf("commit %d, generation %d of %v: not the leader's contract", st.Commit, st.Timing.Generation, st.Timing.ElectionTimeoutMin)
	}
}

// TestAFoundingEntryIsReplacedOnlyByAVerifiedOneOfTheSameMembers: the
// leader's entry 1 replaces this node's only when it verifies, is a
// founding entry, and names the same members; another list, an entry that
// does not verify, or a committed entry 1 keeps the node's own.
func TestAFoundingEntryIsReplacedOnlyByAVerifiedOneOfTheSameMembers(t *testing.T) {
	own := foundingOf(100*time.Millisecond, 1, 2, 3)
	damaged := foundingOf(150*time.Millisecond, 1, 2, 3)
	damaged.Data = append([]byte(nil), damaged.Data...)
	damaged.Data[len(damaged.Data)-1] ^= 1
	cases := []struct {
		name   string
		e1     core.EntryValue
		commit uint64
	}{
		{"another member list", foundingOf(150*time.Millisecond, 1, 2, 4), 0},
		{"a body that does not verify", damaged, 0},
		{"a command at (1, 1)", chainEntries(0, 1, 1)[0], 0},
		{"a committed entry 1", foundingOf(150*time.Millisecond, 1, 2, 3), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTest(2, testState{term: 1, commit: c.commit, ents: []core.EntryValue{own}}, 0, false)
			m := msg(core.MsgApp, 1, 2, 2)
			m.Entries, m.Commit = foundingLeaderLog(c.e1), 1
			f.Step(t0.Add(5*time.Second), m)
			rd := f.Ready()
			if acknowledged(&rd, 2) || f.log.chain(1) != own.ChainSum {
				t.Fatalf("entry 1 replaced: acknowledged %v", acknowledged(&rd, 2))
			}
		})
	}
}

// TestTheSameFoundingEntryIsKept: founders given the same timing write the
// same entry 1, and the leader's append is accepted without a truncation.
func TestTheSameFoundingEntryIsKept(t *testing.T) {
	e1 := foundingOf(150*time.Millisecond, 1, 2, 3)
	f := newTest(2, testState{term: 1, ents: []core.EntryValue{e1}}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Entries, m.Commit = foundingLeaderLog(e1), 2
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	if !acknowledged(&rd, 2) || rd.TruncateFrom != 0 {
		t.Fatalf("acknowledged %v, truncated from %d", acknowledged(&rd, 2), rd.TruncateFrom)
	}
}

// TestAnEntry1MatchedByItsTermAloneIsNeverCommitted: an append whose
// prefix is this founder's uncommitted entry 1, matched by its term alone,
// proves nothing about which entry 1 the leader holds. Acknowledged at 1,
// the leader's heartbeat, its commit clamped to that match, committed the
// founder's own entry 1 while a majority committed the leader's: two
// committed entries at (1, 1). It is refused uncounted with a hint of 0,
// and the append from index 0 that follows proves it either way (#698).
func TestAnEntry1MatchedByItsTermAloneIsNeverCommitted(t *testing.T) {
	own := foundingOf(100*time.Millisecond, 1, 2, 3)
	f := newTest(2, testState{term: 1, ents: []core.EntryValue{own}}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Commit = 1, 1, 1
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	if acknowledged(&rd, 1) {
		t.Fatal("an empty append over entry 1 was acknowledged")
	}
	refused := false
	for _, r := range append(rd.Messages, rd.AfterSync...) {
		refused = refused || (r.Kind == core.MsgAppResp && r.Reject && r.RejectHint == 0)
	}
	if !refused || f.log.committed != 0 {
		t.Fatalf("refused with hint 0 %v, commit %d", refused, f.log.committed)
	}
	if _, _, n := f.ChainRefusal(); n != 0 {
		t.Fatalf("%d chain refusals counted", n)
	}
	hb := msg(core.MsgHeartbeat, 1, 2, 2)
	hb.Commit = 0
	f.Step(t0.Add(5*time.Second+time.Millisecond), hb)
	if f.log.committed != 0 {
		t.Fatalf("commit %d after a heartbeat clamped to no match", f.log.committed)
	}
}

// TestAnotherEntry1ThisNodeKeepsIsNeverAcknowledged: carried alone
// from index 0, a leader's entry 1 this node does not take (another
// member list) was skipped as held on its term, and acknowledged, which
// committed this node's own entry 1 at the next heartbeat. It is held
// back, then refused (TestAFounderOfAnotherMemberListStopsOnceTheLeaderCommitsPastEntry1).
func TestAnotherEntry1ThisNodeKeepsIsNeverAcknowledged(t *testing.T) {
	own := foundingOf(100*time.Millisecond, 1, 2, 3)
	f := newTest(2, testState{term: 1, ents: []core.EntryValue{own}}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Entries, m.Commit = []core.EntryValue{foundingOf(150*time.Millisecond, 1, 2, 4)}, 1
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	if acknowledged(&rd, 1) || f.log.committed != 0 || f.log.chain(1) != own.ChainSum {
		t.Fatalf("acknowledged %v, commit %d", acknowledged(&rd, 1), f.log.committed)
	}
}

// founderAt is a founder whose own InitialTiming is its entry 1's, its
// election window starting at lo.
func founderAt(id core.NodeID, lo time.Duration, e1 core.EntryValue, lease bool) *Raft {
	cfg := &Config{
		ID: id, Group: 1, Seed: 1, Timing: core.TimingContractSpec{
			Generation: 1, HeartbeatInterval: lo / 10, ElectionTimeoutMin: lo, ElectionTimeoutMax: 2 * lo, MaxClockDrift: 0.01,
		},
		MaxInflight: 8, MaxInflightBytes: 1 << 20, MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16,
		Lease: lease, CanVote: true, MaxChainRefusals: 3,
	}
	return NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 1}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: []core.EntryValue{e1}, Now: t0,
	})
}

// out is every message a Ready sends.
func out(rd ReadyValue) []core.MessageValue { return append(rd.Messages, rd.AfterSync...) }

// leaseOverAFounder runs #698's lease scenario on three founders whose
// windows start at 200 ms (the leader, 1), 100 ms (2) and 150 ms (3): 1
// leads term 2 and commits with 3, which took 1's entry 1, then the link
// 1-3 is cut; 2 hears 1's heartbeat round at tH, having taken 1's log
// first when replaced is set. It returns the leader, founder 2, founder 3
// and tH once 2's answer reached the leader.
func leaseOverAFounder(t *testing.T, replaced bool) (l, a, c *Raft, tH time.Time) {
	t.Helper()
	theirs := foundingOf(200*time.Millisecond, 1, 2, 3)
	l = founderAt(1, 200*time.Millisecond, theirs, true)
	now := t0.Add(5 * time.Second)
	l.now, l.term, l.vote = now, 2, 1
	l.becomeLeader()
	rd := l.Ready()
	l.StorageDone(rd.Seq, l.log.lastIndex(), 2)
	l.Ready()
	feed := func(f *Raft, to core.NodeID) {
		app := msg(core.MsgApp, 1, to, 2)
		app.Entries, app.Commit = append([]core.EntryValue(nil), l.log.ents...), l.log.committed
		f.Step(now, app)
		frd := f.Ready()
		f.StorageDone(frd.Seq, f.log.lastIndex(), 2)
		f.Ready()
		ack := msg(core.MsgAppResp, to, 1, 2)
		ack.Index = 2
		l.Step(now, ack)
		l.Ready()
	}
	c = founderAt(3, 150*time.Millisecond, foundingOf(150*time.Millisecond, 1, 2, 3), false)
	feed(c, 3)
	if l.log.committed < 2 {
		t.Fatalf("setup: leader commit %d", l.log.committed)
	}
	a = founderAt(2, 100*time.Millisecond, foundingOf(100*time.Millisecond, 1, 2, 3), false)
	if replaced {
		feed(a, 2)
	}
	tH = now.Add(100 * time.Millisecond)
	l.now = tH
	l.broadcastHeartbeat(tH)
	for _, m := range out(l.Ready()) {
		if m.Kind != core.MsgHeartbeat || m.To != 2 {
			continue
		}
		m.Version, m.Group, m.From, m.Term = 1, 1, 1, 2
		a.Step(tH, m)
		for _, r := range out(a.Ready()) {
			if r.Kind == core.MsgHeartbeatResp {
				r.Version, r.Group, r.From, r.Term = 1, 1, 2, 2
				l.Step(tH, r)
			}
		}
	}
	l.Ready()
	return l, a, c, tH
}

// grantsTerm3 reports whether a grants 3 both its PreVote and its vote of
// term 3 at at, for a log ending at (2, 2).
func grantsTerm3(a *Raft, at time.Time) bool {
	pv := msg(core.MsgPreVote, 3, 2, 3)
	pv.Index, pv.LogTerm = 2, 2
	a.Step(at, pv)
	pre := false
	for _, m := range out(a.Ready()) {
		pre = pre || (m.Kind == core.MsgPreVoteResp && !m.Reject)
	}
	v := pv
	v.Kind = core.MsgVote
	a.Step(at, v)
	rd := a.Ready()
	a.StorageDone(rd.Seq, a.log.lastIndex(), 3)
	vote := false
	for _, m := range append(out(rd), out(a.Ready())...) {
		vote = vote || (m.Kind == core.MsgVoteResp && !m.Reject)
	}
	return pre && vote
}

// TestAFounderBeforeItsEntry1CommitsSizesNoLease: a founder that holds
// its own entry 1, uncommitted, answered a heartbeat with generation 1, its
// own InitialTiming, and the leader sized that answer with its own
// generation 1. Given a shorter timing, the founder promised the leader's
// window and granted a vote inside it: a second leader was elected while
// the first served lease reads (#698's review). It reports generation 0,
// which sizes no lease. The control: once it holds the leader's entry 1
// committed, its answer extends the lease, and it refuses the vote.
func TestAFounderBeforeItsEntry1CommitsSizesNoLease(t *testing.T) {
	l, a, c, tH := leaseOverAFounder(t, false)
	at := tH.Add(120 * time.Millisecond)
	if !c.outsideVoteWindow(at) {
		t.Fatal("setup: founder 3 may not campaign yet")
	}
	if l.LeaseValid(at) && grantsTerm3(a, at) {
		t.Errorf("leader 1 holds a lease of %v at +120ms while founder 2 elects 3 at term 3: two leaders, a stale lease read", l.leaseLen)
	}
	if l.LeaseValid(at) {
		t.Errorf("an answer on no committed contract extended the lease (%v)", l.leaseLen)
	}

	l, a, _, tH = leaseOverAFounder(t, true)
	at = tH.Add(120 * time.Millisecond)
	if !l.LeaseValid(at) {
		t.Fatalf("control: the founder holding the leader's entry 1 committed extends no lease (len %v)", l.leaseLen)
	}
	if grantsTerm3(a, at) {
		t.Error("control: the founder holding the leader's contract granted a vote inside its window")
	}
}

// TestAFounderOfAnotherMemberListStopsOnceTheLeaderCommitsPastEntry1: a
// founder whose entry 1 names another member list never takes the
// leader's, and refused every leader's chain for good while its entry 1
// stayed uncommitted: the divergence stop asked a committed entry, and the
// cluster ran with no failure tolerance, nothing said (#698's review).
// Once a leader that committed past entry 1 shows its own, the founder
// names the divergence; before that the append is held back, unanswered
// and uncounted, for ElectionTimeoutMax, then refused as a broken chain.
func TestAFounderOfAnotherMemberListStopsOnceTheLeaderCommitsPastEntry1(t *testing.T) {
	own, theirs := foundingOf(150*time.Millisecond, 1, 2, 4), foundingOf(150*time.Millisecond, 1, 2, 3)
	leader := append([]core.EntryValue{theirs}, chainEntries(theirs.ChainSum, 2, 2, 2, 2, 2)...)
	whole := func(commit uint64) core.MessageValue {
		m := msg(core.MsgApp, 1, 2, 2)
		m.Entries, m.Commit = leader, commit
		return m
	}
	at := t0.Add(5 * time.Second)

	f := newTest(2, testState{term: 1, ents: []core.EntryValue{own}}, 0, false)
	f.Step(at, whole(5))
	rd := f.Ready()
	if rd.Diverged != (DivergenceValue{Index: 1, Leader: 1}) || answered(&rd) {
		t.Fatalf("a leader at commit 5: Diverged %+v, answered %v", rd.Diverged, answered(&rd))
	}

	g := newTest(2, testState{term: 1, ents: []core.EntryValue{own}}, 0, false)
	for k := range 4 {
		g.Step(at.Add(time.Duration(k)*time.Millisecond), whole(1))
		rd := g.Ready()
		if rd.Diverged.Index != 0 || answered(&rd) {
			t.Fatalf("append %d of a leader at commit 1: Diverged %+v, answered %v", k, rd.Diverged, answered(&rd))
		}
	}
	if _, _, n := g.ChainRefusal(); n != 0 {
		t.Fatalf("%d refusals counted inside the window", n)
	}
	g.Step(at.Add(testTiming().ElectionTimeoutMax), whole(1))
	rd = g.Ready()
	if !answered(&rd) || rd.Diverged.Index != 0 || g.log.chain(1) != own.ChainSum {
		t.Fatalf("past the window: answered %v, Diverged %+v", answered(&rd), rd.Diverged)
	}
	if _, idx, n := g.ChainRefusal(); n != 1 || idx != 1 {
		t.Fatalf("past the window: refusal %d at %d", n, idx)
	}
}

// TestARottedChainPastALeaderCommittedEntry1IsNoDivergence: a node whose
// entry 1 a leader committed, holding it alone, refuses as a broken chain
// an entry 2 whose chain field rotted in the leader's memory, its body
// intact: foundingDivergence judged entry 1 on that chain alone and
// stopped the node BOOTSTRAP_REFUSED (#698's review). The control: a log
// that started holding entry 1 alone, committed, what a data directory
// founded before Bootstrap left it uncommitted holds, still names it.
func TestARottedChainPastALeaderCommittedEntry1IsNoDivergence(t *testing.T) {
	e1 := foundingOf(150*time.Millisecond, 1, 2, 3)
	rotted := chainEntries(e1.ChainSum, 2, 2)
	rotted[0].ChainSum++
	at := t0.Add(5 * time.Second)

	f := newTest(2, testState{term: 1, ents: []core.EntryValue{e1}}, 0, false)
	whole := msg(core.MsgApp, 1, 2, 2)
	whole.Entries, whole.Commit = []core.EntryValue{e1}, 1
	f.Step(at, whole)
	rd := f.Ready()
	f.StorageDone(rd.Seq, 1, 2)
	f.Ready()
	if f.log.committed != 1 || f.log.lastIndex() != 1 {
		t.Fatalf("setup: commit %d last %d", f.log.committed, f.log.lastIndex())
	}
	past := msg(core.MsgApp, 1, 2, 2)
	past.Index, past.LogTerm, past.Entries, past.Commit = 1, 1, rotted, 2
	f.Step(at.Add(time.Millisecond), past)
	rd = f.Ready()
	if rd.Diverged.Index != 0 || !answered(&rd) {
		t.Fatalf("a rotted chain: Diverged %+v, answered %v", rd.Diverged, answered(&rd))
	}
	if _, idx, n := f.ChainRefusal(); n != 1 || idx != 2 {
		t.Fatalf("refusal %d at %d, want one at 2", n, idx)
	}

	g := newTest(2, testState{term: 1, commit: 1, ents: []core.EntryValue{e1}}, 0, false)
	g.Step(at, past)
	if rd := g.Ready(); rd.Diverged != (DivergenceValue{Index: 1, Leader: 1}) {
		t.Fatalf("control: a log founded committed: Diverged %+v", rd.Diverged)
	}
}
