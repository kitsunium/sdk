package raft

import (
	"bytes"
	"fmt"
	"math"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A transport delivers a forward again when it resends a batch its peer had
// partly read: the TCP transport writes a batch up to three times, on the
// link, on the link dialled again and on the back link, eleven seconds
// apart at most under its defaults while its sender runs (#377). The
// answers #401 kept were 4 096, shared by every sender and evicted oldest
// first: a copy that arrived after 4 096 other answers was a new
// submission, appended a second time by the leader that had appended it,
// or by a leader of a later term, or refused NOT_LEADER by a node that held
// it in its log, while its forwarder had taken the first answer (#1482,
// observation O1 of the safety review of #1478). An answer is now kept for
// the forward window, whatever the count, and the fence keeps a later copy
// from being handled again (zwindowfence_internal_test.go). Each test
// answers twice those 4 096 other forwards between a forward and its copy,
// spread over eight seconds, inside the TCP transport's resend window at
// its defaults.

// otherForwards is how many forwards of another sender are answered between
// a forward and its copy: twice the answers #401 kept.
const otherForwards = 2 * 4096

// otherSpread is how many seconds those answers take, one group a second:
// the copy arrives that long after its forward.
const otherSpread = 8

// windowCommand is the command node 2 forwards.
var windowCommand = []byte("c=1")

// windowForward is node 2's forward of windowCommand at term, under ctx.
func windowForward(term, ctx uint64) core.MessageValue {
	m := msg(core.MsgForward, 2, 1, term)
	m.Context = ctx
	m.Entries = []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(windowCommand), Data: windowCommand}}
	return m
}

// windowCopies counts the entries of r's log that carry windowCommand, by
// its body sum: an entry's payload may be evicted from memory, never its
// metadata.
func windowCopies(r *Raft) int {
	sum := wire.BodySum(windowCommand)
	n := 0
	for i := range r.log.ents {
		e := &r.log.ents[i]
		if e.Kind == core.EntryCommand && e.BodySum == sum && (e.Data == nil || bytes.Equal(e.Data, windowCommand)) {
			n++
		}
	}
	return n
}

// answerOtherForwards has r answer otherForwards forwards of node 3 at term,
// one group a second over otherSpread seconds, the first a second after
// now, and drains what they sent.
func answerOtherForwards(r *Raft, term uint64) {
	per := uint64(otherForwards / otherSpread)
	for ctx := uint64(1); ctx <= otherForwards; ctx++ {
		if ctx%per == 1 {
			r.now = r.now.Add(time.Second)
		}
		m := forwardCmd(term, 1<<40+ctx, 1)
		m.From = 3
		r.Step(r.now, m)
		r.Ready()
	}
}

// appendedAtTermTwo makes node 1 the leader of term 2 and has it append
// node 2's forward 9; it returns the position answered.
func appendedAtTermTwo(t *testing.T) (*Raft, core.MessageValue) {
	t.Helper()
	r := leaderOf(2, 0, nil, 0, false)
	r.Step(r.now, windowForward(2, 9))
	as := forwardAnswers(r, 9)
	if len(as) != 1 || as[0].Reject || as[0].LogTerm != 2 || windowCopies(r) != 1 {
		t.Fatalf("setup: term 2 answered %+v, %d copies", as, windowCopies(r))
	}
	return r, as[0]
}

// leadTermFour makes r, which follows term 3, the leader of term 4, its
// no-op durable.
func leadTermFour(r *Raft) {
	r.now = r.now.Add(time.Second)
	r.term, r.vote = 4, 1
	r.becomeLeader()
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), 4)
	r.Ready()
}

// The leader that appended a forward meets its copy after twice 4 096 other
// answers, in the same term: the copy is answered with the first position
// and nothing is appended. Red on main since #401.
func TestACopyAfterManyOtherForwardsIsNotAppendedAgain(t *testing.T) {
	r, first := appendedAtTermTwo(t)
	answerOtherForwards(r, 2)
	r.Step(r.now, windowForward(2, 9))
	as := forwardAnswers(r, 9)
	if n := windowCopies(r); n != 1 {
		t.Fatalf("the copy, %d other answers and %d s after its forward: answered %+v; the command appended %d times (first at %d/%d)", otherForwards, otherSpread, as, n, first.LogTerm, first.Index)
	}
	if len(as) != 1 || as[0].Reject || as[0].Index != first.Index || as[0].LogTerm != first.LogTerm {
		t.Fatalf("the copy answered %+v, want the first answer %+v", as, first)
	}
}

// The leader of term 2 appended a forward, followed term 3 while it answered
// twice 4 096 other forwards, and leads term 4 when the copy of the term-2
// frame arrives: the copy is answered with the term-2 position and nothing
// is appended. Red since #1478 handles an older term's forward.
func TestAnOlderTermCopyAfterManyOtherForwardsIsNotAppendedAgain(t *testing.T) {
	r, first := appendedAtTermTwo(t)
	r.becomeFollower(3, 0)
	answerOtherForwards(r, 3)
	leadTermFour(r)
	r.Step(r.now, windowForward(2, 9))
	as := forwardAnswers(r, 9)
	if n := windowCopies(r); n != 1 {
		t.Fatalf("the term-2 copy at the leader of term 4: answered %+v; the command appended %d times (first at %d/%d)", as, n, first.LogTerm, first.Index)
	}
	if len(as) != 1 || as[0].Reject || as[0].Index != first.Index || as[0].LogTerm != first.LogTerm {
		t.Fatalf("the term-2 copy answered %+v, want the term-2 answer %+v", as, first)
	}
}

// The same, the node following term 3 when the copy arrives: a refusal says
// nothing was appended (D6's one exception to OUTCOME_UNKNOWN), and this node
// appended it, so the copy is answered with its position, never refused.
// Red since #1478 handles an older term's forward.
func TestAnOlderTermCopyAfterManyOtherForwardsIsNotRefused(t *testing.T) {
	r, first := appendedAtTermTwo(t)
	r.becomeFollower(3, 0)
	answerOtherForwards(r, 3)
	r.Step(r.now, windowForward(2, 9))
	as := forwardAnswers(r, 9)
	for _, a := range as {
		if a.Reject {
			t.Fatalf("the term-2 copy at a follower of term 3 is refused %+v (code %v), while the command sits at %d/%d in its log", a, a.Code, first.LogTerm, first.Index)
		}
	}
	if len(as) != 1 || as[0].Index != first.Index || as[0].LogTerm != first.LogTerm {
		t.Fatalf("the term-2 copy answered %+v, want the term-2 answer %+v", as, first)
	}
}

// A node that does not run — a machine or a process paused — leaves the
// copies its peers' transports resent waiting in its sockets, and steps them
// when it wakes, its clock a minute later. The table's clock counts at most
// fwdClockStep of a gap between two readings, so the pause ages no answer.
// checkPausedTableKeepsItsAnswer plays it on a table whose clock moves at
// most step between two readings: fwdClockStep, or, the mutation, the whole
// gap, the driver's clock as it reads. It returns an error when the answer
// is gone once the node wakes.
func checkPausedTableKeepsItsAnswer(step time.Duration) error {
	var tab fwdTable
	tab.advance(t0, step)
	k := fwdKey{from: 2, ctx: 9}
	tab.remember(k, fwdAnswer{index: 2, logTerm: 2})
	tab.advance(t0.Add(time.Minute), step)
	tab.expire(defaultForwardWindow)
	if _, ok := tab.lookup(k); !ok {
		return fmt.Errorf("the answer is gone after a pause of a minute: the table's clock moved %v", tab.clock)
	}
	return nil
}

// TestAPausedNodeKeepsItsForwardAnswers: the pause ages no answer, and a
// table aged by the driver's clock as it reads is caught; through the step
// function, the leader that answered a forward and then did not run for a
// minute answers its copy with the first position and proposes nothing.
func TestAPausedNodeKeepsItsForwardAnswers(t *testing.T) {
	if err := checkPausedTableKeepsItsAnswer(fwdClockStep); err != nil {
		t.Fatal(err)
	}
	err := checkPausedTableKeepsItsAnswer(math.MaxInt64)
	if err == nil {
		t.Fatal("a table aged by the driver's clock as it reads kept its answer across the pause: the check catches nothing")
	}
	t.Logf("the driver's clock as it reads is caught: %v", err)
	r, first := appendedAtTermTwo(t)
	r.now = r.now.Add(time.Minute)
	r.Step(r.now, windowForward(2, 9))
	as := forwardAnswers(r, 9)
	if n := windowCopies(r); n != 1 || len(as) != 1 || as[0].Index != first.Index || as[0].LogTerm != first.LogTerm {
		t.Fatalf("the copy stepped after a pause of a minute: answered %+v, %d copies, want the first answer %+v and one copy", as, n, first)
	}
}

// An answer is kept the whole forward window and no longer: the table's
// clock, read at each Tick, keeps it until the window ends, then forgets it,
// so the memory the table holds is that of the window's forwards.
func TestAForwardAnswerIsKeptTheWholeWindow(t *testing.T) {
	// The node runs at term 3, heard from its leader, not restarted at it:
	// a run handles no forward of the term it started at (fwdFence).
	r := newTest(1, testState{term: 2}, 0, false)
	r.Step(r.now, msg(core.MsgHeartbeat, 3, 1, 3))
	r.Ready()
	r.Step(r.now, windowForward(3, 9))
	first := forwardAnswers(r, 9)
	if len(first) != 1 || !first[0].Reject {
		t.Fatalf("setup: a follower answered %+v, want a refusal", first)
	}
	window := r.forwardWindow()
	at := r.now
	for d := 100 * time.Millisecond; d < window; d += 100 * time.Millisecond {
		r.Tick(at.Add(d))
		r.Ready()
	}
	if _, kept := r.fwdAnswers.lookup(fwdKey{from: 2, ctx: 9}); !kept {
		t.Fatalf("the answer is gone before the window of %v ends", window)
	}
	r.Tick(at.Add(window))
	if _, kept := r.fwdAnswers.lookup(fwdKey{from: 2, ctx: 9}); kept || r.fwdAnswers.kept() != 0 {
		t.Fatalf("the answer outlived the window of %v: %d answers kept", window, r.fwdAnswers.kept())
	}
}

// lateCopies is the forwarded workload of
// TestForwardedWritesUnderDuplicationAreAppliedOnce with the copy of a
// forward delivered up to nine seconds after its original — the TCP
// transport's resend window under its defaults — and every node's answers
// kept for window, 0 the default.
func lateCopies(seed uint64, window time.Duration) simOptions {
	return simOptions{seed: seed, drop: 0.03, dup: 0.25, crashRate: 0.3, fwdLate: 9 * time.Second, fwdWindow: window}
}

// pastTheWindow is lateCopies with answers kept 50 ms only, so nearly every
// copy comes past the window, a copy reaching its node after it restarted
// too (review D1 (c)), and the fence off when unfenced: the node of the
// release before #1482's.
func pastTheWindow(seed uint64, unfenced bool) simOptions {
	o := lateCopies(seed, 50*time.Millisecond)
	o.fwdAcross, o.fwdUnfenced = true, unfenced
	return o
}

// TestForwardedWritesWithLateCopiesAreAppliedOnce: across partitions,
// crashes and changes of leader, a copy that arrives up to the resend
// window after its forward is answered from the table, and no command is
// applied twice.
func TestForwardedWritesWithLateCopiesAreAppliedOnce(t *testing.T) {
	eachSeed(t, seeds(t, 8), func(t *testing.T, seed uint64) {
		if err := forwardedRunOver(lateCopies(seed, 0)); err != nil {
			t.Fatal(err)
		}
	})
}

// TestForwardedLateCopiesPastTheWindowAreFenced: the copies come past the
// window, at a node that restarted since too; the fence handles none of
// them again, and no command is applied twice, no refused write applied.
func TestForwardedLateCopiesPastTheWindowAreFenced(t *testing.T) {
	eachSeed(t, seeds(t, 8), func(t *testing.T, seed uint64) {
		if err := forwardedRunOver(pastTheWindow(seed, false)); err != nil {
			t.Fatal(err)
		}
	})
}

// TestForwardedLateCopiesInAMixedClusterAreFenced runs the same in a mixed
// cluster: node 3 speaks protocol 2 and node 2 protocol 3, so the cluster
// version stays below what node 1 speaks; the fence crosses no wire and
// needs no raise, and it holds at every version.
func TestForwardedLateCopiesInAMixedClusterAreFenced(t *testing.T) {
	eachSeed(t, seeds(t, 8), func(t *testing.T, seed uint64) {
		o := pastTheWindow(seed, false)
		o.speaks = map[core.NodeID]uint32{2: Protocol3, 3: Protocol2}
		if err := forwardedRunOver(o); err != nil {
			t.Fatal(err)
		}
	})
}

// TestForwardedLateCopiesPastTheWindowAreCaught is the scenario's control:
// without the fence, as the release before #1482's ran, a copy past the
// window is a new submission, and some command is applied twice, or a
// refused write applied, and the run says so.
func TestForwardedLateCopiesPastTheWindowAreCaught(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		if err := forwardedRunOver(pastTheWindow(seed, true)); err != nil {
			t.Logf("caught: %v", err)
			return
		}
	}
	t.Fatal("no seed applied a late copy twice without the fence: the scenario delivers no copy past the window")
}
