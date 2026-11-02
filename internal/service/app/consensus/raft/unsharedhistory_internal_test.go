package raft

import (
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// foundingOf is an entry 1 at (1, 1) carrying tag, consistent with itself:
// founders that passed Bootstrap different timings each hold one, committed,
// and no two of them are the same (#698).
func foundingOf(tag string) core.EntryValue {
	d := []byte("founding-" + tag)
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 1}, Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}
	e.ChainSum = wire.ChainSum(0, &e)
	return e
}

// founderOn starts node 2 over the founding entry e1, committed at term 1,
// as Bootstrap leaves every founder.
func founderOn(e1 core.EntryValue) *Raft {
	return newTest(2, testState{term: 1, commit: 1, ents: []core.EntryValue{e1}}, 0, false)
}

// steppedBack is the append leader 1 of term sends once node 2 refused the
// chain of its no-op at index 2: from index 0, its own entry 1 e1 first.
func steppedBack(term uint64, e1 core.EntryValue) core.MessageValue {
	m := msg(core.MsgApp, 1, 2, term)
	m.Entries = append([]core.EntryValue{e1}, chainEntries(e1.ChainSum, 2, term)...)
	m.Commit = 2
	return m
}

// answersOf returns what node 2 sent leader 1 of kind in rd.
func answersOf(rd *ReadyValue, kind core.MessageKind) []core.MessageValue {
	var out []core.MessageValue
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.To == 1 && m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// TestAFounderRefusesForGoodALeaderFoundedOnAnotherEntry is #698 at the
// step function: node 2's committed entry 1 and leader 1's are both at
// (1, 1), each consistent with itself from the chain's seed, and they
// differ. That proves two histories, which no truncation reconciles: the
// node answered the leader's every append with a chain refusal, for as long
// as the cluster lived, and voted for it at the next election. It now
// refuses that member for good, its appends and heartbeats unanswered and
// not taken as contact, its votes refused, at this term and at every later
// one, and names it in the Ready once.
func TestAFounderRefusesForGoodALeaderFoundedOnAnotherEntry(t *testing.T) {
	f := founderOn(foundingOf("a"))
	at := t0.Add(5 * time.Second)
	f.Step(at, steppedBack(2, foundingOf("b")))
	rd := f.Ready()
	if got := answersOf(&rd, core.MsgAppResp); len(got) != 0 {
		t.Errorf("the leader founded on another entry 1 was answered %+v", got)
	}
	want := []HistoryConflictValue{{From: 1, Index: 1, Term: 1}}
	if !slices.Equal(rd.HistoryConflicts, want) || !slices.Equal(f.HistoryRefusals(), want) {
		t.Errorf("Ready named %v, the node refuses %v: want %v", rd.HistoryConflicts, f.HistoryRefusals(), want)
	}
	if f.log.lastIndex() != 1 {
		t.Errorf("the node appended past its entry 1 (last %d)", f.log.lastIndex())
	}
	f.Step(at, msg(core.MsgHeartbeat, 1, 2, 2))
	if rd := f.Ready(); len(answersOf(&rd, core.MsgHeartbeatResp)) != 0 {
		t.Error("the refused leader's heartbeat was answered")
	}
	// The next term starts no new chance: the two entries 1 still differ.
	later := at.Add(3 * time.Second)
	f.Step(later, steppedBack(3, foundingOf("b")))
	if rd := f.Ready(); len(answersOf(&rd, core.MsgAppResp)) != 0 {
		t.Error("the refused member, leader again at a later term, was answered")
	}
	vote := msg(core.MsgPreVote, 1, 2, 4)
	vote.LogTerm, vote.Index = 3, 2
	f.Step(later.Add(3*time.Second), vote)
	if rd := f.Ready(); granted(rd, core.MsgPreVoteResp) {
		t.Error("the refused member was granted a PreVote")
	}
	vote.Kind = core.MsgVote
	f.Step(later.Add(3*time.Second), vote)
	if rd := f.Ready(); granted(rd, core.MsgVoteResp) {
		t.Error("the refused member was granted a vote")
	}
	if f.term > 3 {
		t.Errorf("the refused member's vote moved the term to %d", f.term)
	}
	if rd := f.Ready(); len(rd.HistoryConflicts) != 0 {
		t.Errorf("the standing refusal was named again: %v", rd.HistoryConflicts)
	}
}

// TestAFounderFollowsALeaderFoundedOnTheSameEntry is the control: the same
// stepped-back append from a leader founded on the same entry 1 is re-based
// on the commit, as a silent node needs (rebaseOnCommit), and acknowledged.
func TestAFounderFollowsALeaderFoundedOnTheSameEntry(t *testing.T) {
	f := founderOn(foundingOf("a"))
	f.Step(t0.Add(5*time.Second), steppedBack(2, foundingOf("a")))
	rd := f.Ready()
	f.StorageDone(rd.Seq, 2, 2)
	rd2 := f.Ready()
	acks := append(answersOf(&rd, core.MsgAppResp), answersOf(&rd2, core.MsgAppResp)...)
	if len(acks) != 1 || acks[0].Reject || acks[0].Index != 2 {
		t.Fatalf("the append was answered %+v, want one acknowledgement of index 2", acks)
	}
	if len(rd.HistoryConflicts) != 0 || f.HistoryRefusals() != nil {
		t.Fatalf("a leader of the same history was refused: %v", rd.HistoryConflicts)
	}
}

// TestALeaderThatOverwritesAnUncommittedTailIsFollowed is the control of an
// ordinary truncation: the node holds entries 2 and 3 of term 1, never
// committed, and the leader of term 2 holds another entry 2 of its own
// term. That conflict is Raft's normal repair, never another history: the
// node truncates and acknowledges, whether the append starts at its commit
// or below it.
func TestALeaderThatOverwritesAnUncommittedTailIsFollowed(t *testing.T) {
	for _, prev := range []uint64{0, 1} {
		e1 := foundingOf("a")
		f := newTest(2, testState{term: 1, commit: 1, ents: append([]core.EntryValue{e1}, chainEntries(e1.ChainSum, 2, 1, 1)...)}, 0, false)
		m := steppedBack(2, e1)
		if prev == 1 {
			m.Index, m.LogTerm, m.Entries = 1, 1, m.Entries[1:]
		}
		f.Step(t0.Add(5*time.Second), m)
		rd := f.Ready()
		if rd.TruncateFrom != 2 || f.log.lastIndex() != 2 {
			t.Fatalf("from %d: truncated from %d, last %d: want the uncommitted tail replaced from 2", prev, rd.TruncateFrom, f.log.lastIndex())
		}
		f.StorageDone(rd.Seq, 2, 2)
		rd2 := f.Ready()
		acks := append(answersOf(&rd, core.MsgAppResp), answersOf(&rd2, core.MsgAppResp)...)
		if len(acks) != 1 || acks[0].Reject || acks[0].Index != 2 {
			t.Fatalf("from %d: the leader was answered %+v, want one acknowledgement of index 2", prev, acks)
		}
		f.Step(t0.Add(5*time.Second), msg(core.MsgHeartbeat, 1, 2, 2))
		if rd := f.Ready(); len(answersOf(&rd, core.MsgHeartbeatResp)) != 1 {
			t.Fatalf("from %d: the leader's heartbeat went unanswered after a truncation", prev)
		}
		if len(rd.HistoryConflicts) != 0 || f.HistoryRefusals() != nil {
			t.Fatalf("from %d: a truncation was taken for another history: %v", prev, rd.HistoryConflicts)
		}
	}
}

// TestAnEntryThatFailsItsOwnSumsIsAChainRefusal: the leader's entry at the
// node's commit differs from the node's, but its payload no longer matches
// its sum, so it proves nothing but a damaged entry (D4). The node refuses
// the append as a broken chain and answers, and the leader resends, the
// same as for any entry that does not verify.
func TestAnEntryThatFailsItsOwnSumsIsAChainRefusal(t *testing.T) {
	f := founderOn(foundingOf("a"))
	m := steppedBack(2, foundingOf("b"))
	m.Entries[0].Data = []byte("founding-c")
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	refusals := answersOf(&rd, core.MsgAppResp)
	if len(refusals) != 1 || !refusals[0].Reject || len(rd.ChainRefusedBy) != 1 {
		t.Fatalf("a damaged entry was answered %+v, chain refusals %v: want one chain refusal", refusals, rd.ChainRefusedBy)
	}
	if len(rd.HistoryConflicts) != 0 || f.HistoryRefusals() != nil {
		t.Fatalf("a damaged entry was taken for another history: %v", rd.HistoryConflicts)
	}
}

// TestAHeldEntryWhoseChainDiffersIsNeverSkipped: the node holds entry 2 of
// term 1 past its commit, and the leader sends an entry 2 of term 1 too,
// with the same body, chained from another entry 1. firstNewEntry skipped
// any entry held at the same term and took the message's chain value as its
// own: the leader's entry 3 then verified against it and was appended after
// the node's own entry 2, a log whose chain broke at 3, and the two
// histories diverged in silence. The entry is refused as a broken chain.
func TestAHeldEntryWhoseChainDiffersIsNeverSkipped(t *testing.T) {
	a, b := foundingOf("a"), foundingOf("b")
	f := newTest(2, testState{term: 1, commit: 1, ents: append([]core.EntryValue{a}, chainEntries(a.ChainSum, 2, 1)...)}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm = 1, 1
	m.Entries = chainEntries(b.ChainSum, 2, 1, 2)
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	if f.log.lastIndex() != 2 {
		t.Fatalf("the node appended the leader's entry 3 after its own entry 2 (last %d)", f.log.lastIndex())
	}
	if refusals := answersOf(&rd, core.MsgAppResp); len(refusals) != 1 || !refusals[0].Reject {
		t.Fatalf("the append was answered %+v: want one refusal", refusals)
	}
	if len(rd.HistoryConflicts) != 0 {
		t.Fatalf("an entry that does not verify from this log's chain was taken for another history: %v", rd.HistoryConflicts)
	}
}

// TestAHeldEntryOfTheSameTermWithAnotherBodyProvesAnotherHistory: the node
// holds entry 2 of term 1 past its commit, and the leader an entry 2 of term
// 1 with another body, both consistent with themselves from the same entry
// 1. One leader writes one entry per index in its term: two such entries
// are two histories. firstNewEntry skipped it as held and acknowledged index
// 2, which the leader counted as holding its own.
func TestAHeldEntryOfTheSameTermWithAnotherBodyProvesAnotherHistory(t *testing.T) {
	a := foundingOf("a")
	held := chainEntries(a.ChainSum, 2, 1)
	f := newTest(2, testState{term: 1, commit: 1, ents: append([]core.EntryValue{a}, held...)}, 0, false)
	other := held[0]
	other.Data = []byte("another body")
	other.BodySum = wire.BodySum(other.Data)
	other.ChainSum = wire.ChainSum(a.ChainSum, &other)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Commit = 1, 1, 2
	m.Entries = []core.EntryValue{other}
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	if acks := answersOf(&rd, core.MsgAppResp); len(acks) != 0 {
		t.Fatalf("the leader holding another entry (2, 1) was answered %+v", acks)
	}
	if f.log.committed != 1 {
		t.Fatalf("the node committed its own entry 2 on the leader's word (commit %d)", f.log.committed)
	}
	if want := []HistoryConflictValue{{From: 1, Index: 2, Term: 1}}; !slices.Equal(rd.HistoryConflicts, want) {
		t.Fatalf("Ready named %v, want %v", rd.HistoryConflicts, want)
	}
}

// TestAHistoryRefusalEndsWithTheEntryItCompared: the refusal is the proof,
// and the proof is this log's entry. Once the leader's snapshot moved the
// log past that entry, the two logs share the snapshot's chain tip, the
// refusal ends, and the member's next append is acknowledged; the node never
// refuses a leader on an entry it no longer holds.
func TestAHistoryRefusalEndsWithTheEntryItCompared(t *testing.T) {
	f := founderOn(foundingOf("a"))
	m := steppedBack(2, foundingOf("b"))
	at := t0.Add(5 * time.Second)
	f.Step(at, m)
	f.Ready()
	if f.HistoryRefusals() == nil {
		t.Fatal("setup: the leader founded on another entry 1 was not refused")
	}
	tip := m.Entries[1]
	f.InstallSnapshot(core.SnapshotMetaValue{Position: tip.Position, ChainTip: tip.ChainSum, Membership: testConf(1, 2, 3), Timing: testTiming()})
	f.Ready()
	if got := f.HistoryRefusals(); got != nil {
		t.Fatalf("the refusal outlived the entry it compared: %v", got)
	}
	next := msg(core.MsgApp, 1, 2, 2)
	next.Index, next.LogTerm, next.Commit = 2, 2, 3
	next.Entries = chainEntries(tip.ChainSum, 3, 2)
	f.Step(at, next)
	rd := f.Ready()
	if acks := answersOf(&rd, core.MsgAppResp); len(acks) != 1 || acks[0].Reject || acks[0].Index != 3 {
		t.Fatalf("the member's append after the install was answered %+v, want one acknowledgement of index 3", acks)
	}
}
