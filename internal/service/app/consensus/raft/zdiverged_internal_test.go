package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// ownFounding is a founder's entry 1, committed, that differs from the
// leader's (chainEntries' at index 1): another founder's timing (#698).
func ownFounding() []core.EntryValue {
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 1}, Kind: core.EntryCommand, Data: []byte("own timing")}
	e.BodySum = wire.BodySum(e.Data)
	e.ChainSum = wire.ChainSum(0, &e)
	return []core.EntryValue{e}
}

// answered reports whether rd answers the leader 1 with an append response.
func answered(rd *ReadyValue) bool {
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.To == 1 && m.Kind == core.MsgAppResp {
			return true
		}
	}
	return false
}

// TestAnAppendOfALeaderThatCommittedPastAnotherEntry1IsADivergence: a
// founder whose entry 1 differs from the leader's is told so by the first
// append of a leader whose commit passed entry 1, re-based on the commit
// (prev 0) or past it (prev 1): Ready.Diverged names index 1 and the
// append is not answered.
func TestAnAppendOfALeaderThatCommittedPastAnotherEntry1IsADivergence(t *testing.T) {
	leader := chainEntries(0, 1, 1, 2)
	cases := []struct {
		name string
		prev uint64
		ents []core.EntryValue
	}{
		{"re-based on the commit", 0, leader},
		{"past entry 1", 1, leader[1:]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTest(2, testState{term: 1, commit: 1, ents: ownFounding()}, 0, false)
			m := msg(core.MsgApp, 1, 2, 2)
			m.Index, m.LogTerm, m.Entries, m.Commit = c.prev, 1, c.ents, 2
			if c.prev == 0 {
				m.LogTerm = 0
			}
			f.Step(t0.Add(5*time.Second), m)
			if !f.HasReady() {
				t.Fatal("a divergence alone is no Ready: the driver never stops")
			}
			rd := f.Ready()
			if rd.Diverged != (DivergenceValue{Index: 1, Leader: 1}) {
				t.Fatalf("Diverged %+v", rd.Diverged)
			}
			if answered(&rd) {
				t.Error("the append that proved the divergence was answered")
			}
		})
	}
}

// TestADivergenceNoCommitProvesIsHeldBackThenRefused: a leader that
// committed nothing past entry 1 proves nothing. Its appends go unanswered
// and uncounted for ElectionTimeoutMax, the window a leader of the majority
// has to commit, then are refused as a broken chain (D4).
func TestADivergenceNoCommitProvesIsHeldBackThenRefused(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 1, ents: ownFounding()}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Entries, m.Commit = 1, 1, chainEntries(0, 1, 1, 2)[1:], 1
	at := t0.Add(5 * time.Second)
	for k := range 4 {
		f.Step(at.Add(time.Duration(k)*time.Millisecond), m)
		rd := f.Ready()
		if rd.Diverged.Index != 0 || answered(&rd) {
			t.Fatalf("append %d inside the window: Diverged %+v, answered %v", k, rd.Diverged, answered(&rd))
		}
	}
	if _, _, n := f.ChainRefusal(); n != 0 {
		t.Fatalf("%d refusals counted inside the window", n)
	}
	f.Step(at.Add(testTiming().ElectionTimeoutMax), m)
	rd := f.Ready()
	if !answered(&rd) || rd.Diverged.Index != 0 {
		t.Fatalf("past the window: answered %v, Diverged %+v", answered(&rd), rd.Diverged)
	}
	if _, idx, n := f.ChainRefusal(); n != 1 || idx != 2 {
		t.Fatalf("past the window: refusal %d at %d", n, idx)
	}
}

// TestAnEntry1ThatDoesNotVerifyIsNoDivergence: a leader's entry 1 whose
// sums fail is damage, not another history, and is refused as before.
func TestAnEntry1ThatDoesNotVerifyIsNoDivergence(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 1, ents: ownFounding()}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Entries, m.Commit = chainEntries(0, 1, 1, 2), 2
	m.Entries[0].ChainSum++
	m.Entries[1].BodySum++
	f.Step(t0.Add(5*time.Second), m)
	if rd := f.Ready(); rd.Diverged.Index != 0 {
		t.Fatalf("Diverged %+v on damaged entries", rd.Diverged)
	}
}

// TestTheSameEntry1IsNoDivergence: the founders agree, and the append is
// accepted.
func TestTheSameEntry1IsNoDivergence(t *testing.T) {
	leader := chainEntries(0, 1, 1, 2)
	f := newTest(2, testState{term: 1, commit: 1, ents: leader[:1]}, 0, false)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Entries, m.Commit = leader, 2
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	if rd.Diverged.Index != 0 || f.log.lastIndex() != 2 {
		t.Fatalf("Diverged %+v, last %d", rd.Diverged, f.log.lastIndex())
	}
}
