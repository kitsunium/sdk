package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A forward of an older term goes on at the receiver's term (#1440), and
// its answer keeps the forward's term. Stamped with the receiver's later
// term, the answer raised the forwarder's: a follower that had just heard
// its live leader adopted a term with no vote and no leader, past D5's
// live-leader refusal, then granted a vote of that term, and a lease read
// on the leader it had acknowledged missed a write the new leader
// committed (D9 (b), R1-18). The mutation type is full, so each test takes
// its mutation as an argument: the answer re-stamped, in flight, with its
// sender's term, which is what the step function sent before the fix, read
// by a node run as a release before #1472 (termFromAnswers), which takes
// the term an answer carries: read by this release, a re-stamped answer
// is dropped (stepLaterAnswer), and nothing is left to catch.

// checkForwardAnswerRaisesNoTerm plays the mechanism on two step functions.
// F (node 2) forwarded a command at term 2, then followed G (node 1) at
// term 3 and has just heard it; X (node 3), at term 4, answers the forward;
// then X's vote request of term 4 reaches F. restamp gives the answer X's
// term, and runs F as a previous release. It returns an error when F grants
// the vote, moves past term 3, or is answered at another term than its
// forward's.
func checkForwardAnswerRaisesNoTerm(restamp bool) error {
	now := t0.Add(5 * time.Second)
	ents := chainEntries(0, 1, 1)
	f := newTest(2, testState{term: 2, commit: 1, ents: ents}, 0, false)
	f.cfg.termFromAnswers = restamp
	hb := msg(core.MsgHeartbeat, 1, 2, 3)
	hb.Commit = 1
	f.Step(now, hb)
	if st := f.Status(); st.Term != 3 || st.Lead != 1 {
		return fmt.Errorf("setup: F at term %d following %d, want term 3 following G", st.Term, st.Lead)
	}
	f.Ready()
	x := newTest(3, testState{term: 4, commit: 1, ents: ents}, 0, false)
	fwd := forwardCmd(2, 9, 10)
	fwd.To = 3
	x.Step(now, fwd)
	as := forwardAnswers(x, 9)
	if len(as) != 1 {
		return fmt.Errorf("setup: the forward was answered %d times: %+v", len(as), as)
	}
	answer := as[0]
	if restamp {
		answer.Term = x.term
	}
	f.Step(now, answer)
	vote := msg(core.MsgVote, 3, 2, 4)
	vote.Index, vote.LogTerm = 1, 1
	f.Step(now, vote)
	rd := f.Ready()
	switch {
	case granted(rd, core.MsgVoteResp):
		return fmt.Errorf("F, which heard live leader G at term 3, read X's answer at term %d and granted X's vote of term 4", answer.Term)
	case f.term != 3:
		return fmt.Errorf("the answer to a forward of term 2 moved its forwarder from term 3 to %d", f.term)
	case answer.Term != fwd.Term:
		return fmt.Errorf("a forward of term %d answered at term %d: %+v", fwd.Term, answer.Term, answer)
	}
	return nil
}

// TestAForwardAnswerNeverRaisesTheForwardersTerm: the answer keeps the
// forward's term, and the forwarder refuses the next vote for its live
// leader; re-stamped with the receiver's term, the same answer makes it
// grant that vote, and the check says so.
func TestAForwardAnswerNeverRaisesTheForwardersTerm(t *testing.T) {
	if err := checkForwardAnswerRaisesNoTerm(false); err != nil {
		t.Fatal(err)
	}
	err := checkForwardAnswerRaisesNoTerm(true)
	if err == nil {
		t.Fatal("an answer re-stamped with the receiver's term was not caught")
	}
	t.Logf("the answer at the receiver's term is caught: %v", err)
}

// lateVerdict is what lateNet does with one message.
type lateVerdict int

const (
	lateDeliver lateVerdict = iota
	lateDrop
	lateHold
)

const (
	lateG core.NodeID = 1 // leader of term 3, holds the lease
	lateF core.NodeID = 2 // follower that forwarded a command at term 2
	lateX core.NodeID = 3 // leader of term 2, later candidate of term 4
)

// lateNet is a network of three step functions whose every message a
// policy delivers, drops or holds: D15's fault model, delays, losses and
// reordering between lanes, played by hand. restamp re-stamps every
// forward's answer with its sender's term.
type lateNet struct {
	t       *testing.T
	nodes   map[core.NodeID]*Raft
	ids     []core.NodeID
	now     time.Time
	held    []core.MessageValue
	policy  func(m core.MessageValue) lateVerdict
	restamp bool
}

// lateNode is a voter of members 1, 2 and 3 with the lease on.
func lateNode(id core.NodeID, seed uint64) *Raft {
	tm := testTiming()
	cfg := &Config{
		ID: id, Group: 1, Seed: seed, Timing: tm, MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, Lease: true, CanVote: true, MaxChainRefusals: 3,
	}
	return NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 1, Commit: 1},
		Snapshot: core.SnapshotMetaValue{
			Position: core.PositionValue{Term: 1, Index: 1}, Membership: testConf(1, 2, 3), Timing: tm,
		},
		Now: t0,
	})
}

// lateDrain takes r's Ready until it is empty, every write durable at once,
// and returns what r sends, in order.
func lateDrain(r *Raft) []core.MessageValue {
	var out []core.MessageValue
	for i := 0; i < 256 && r.HasReady(); i++ {
		rd := r.Ready()
		out = append(out, rd.Messages...)
		if rd.NeedsWrite() {
			var idx, term uint64
			if n := len(rd.Entries); n > 0 {
				idx, term = rd.Entries[n-1].Position.Index, rd.Entries[n-1].Position.Term
			}
			r.StorageDone(rd.Seq, idx, term)
		}
		out = append(out, rd.AfterSync...)
	}
	return out
}

// pump hands every node's messages to the policy until none is sent.
func (n *lateNet) pump() {
	for round := 0; round < 256; round++ {
		var out []core.MessageValue
		for _, id := range n.ids {
			out = append(out, lateDrain(n.nodes[id])...)
		}
		if len(out) == 0 {
			return
		}
		for _, m := range out {
			if n.restamp && (m.Kind == core.MsgForwardResp || m.Kind == core.MsgForwardAdminResp) {
				m.Term = n.nodes[m.From].term
			}
			switch n.policy(m) {
			case lateDeliver:
				n.nodes[m.To].Step(n.now, m)
			case lateHold:
				n.held = append(n.held, m)
			default:
			}
		}
	}
	n.t.Fatal("the network never settled")
}

// tick advances every node by 20 ms steps for at most d, stopping early
// when stop reports true after a step.
func (n *lateNet) tick(d time.Duration, stop func() bool) bool {
	end := n.now.Add(d)
	for n.now.Before(end) {
		n.now = n.now.Add(20 * time.Millisecond)
		for _, id := range n.ids {
			n.nodes[id].Tick(n.now)
		}
		n.pump()
		if stop != nil && stop() {
			return true
		}
	}
	return false
}

// take removes the held messages f selects and returns them.
func (n *lateNet) take(f func(core.MessageValue) bool) []core.MessageValue {
	var out, keep []core.MessageValue
	for _, m := range n.held {
		if f(m) {
			out = append(out, m)
		} else {
			keep = append(keep, m)
		}
	}
	n.held = keep
	return out
}

// deliver steps ms at their recipients now, then pumps.
func (n *lateNet) deliver(ms ...core.MessageValue) {
	for _, m := range ms {
		n.nodes[m.To].Step(n.now, m)
	}
	n.pump()
}

// newLateNet starts G, F and X under seed, each node before names run as a
// release before #1472 (termFromAnswers).
func newLateNet(t *testing.T, seed uint64, before ...core.NodeID) *lateNet {
	n := &lateNet{t: t, nodes: map[core.NodeID]*Raft{}, ids: []core.NodeID{lateG, lateF, lateX}, now: t0}
	for _, id := range n.ids {
		n.nodes[id] = lateNode(id, seed)
	}
	for _, id := range before {
		n.nodes[id].cfg.termFromAnswers = true
	}
	return n
}

// lateForwardSchedule plays one schedule of the review of #1457 (D1) and
// reports whether seed reached it, and the lease violation it found, if
// any. X leads term 2 and F forwards it a command whose frame is delayed;
// G wins term 3 with F; G's next round renews its lease on F's
// acknowledgement while X, a candidate of term 4, finally answers the
// forward; then X's delayed vote request of term 4 reaches F. before names
// the nodes run as a release before #1472.
func lateForwardSchedule(t *testing.T, seed uint64, restamp bool, before ...core.NodeID) (bool, string) {
	n := newLateNet(t, seed, before...)
	n.restamp = restamp
	c, ok := lateForwardSetup(n, seed)
	if !ok {
		return false, ""
	}
	return lateRace(n, c)
}

// lateForwardSetup plays steps 1 to 3 of the forward's schedule and returns
// F's forward of term 2, held in the network, and whether seed reached G's
// leadership of term 3.
func lateForwardSetup(n *lateNet, seed uint64) (core.MessageValue, bool) {
	g, f, x := n.nodes[lateG], n.nodes[lateF], n.nodes[lateX]

	// 1. X wins term 2: the PreVotes of G and F are lost.
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.Kind == core.MsgPreVote && m.From != lateX {
			return lateDrop
		}
		return lateDeliver
	}
	if !n.tick(5*time.Second, func() bool {
		st := x.Status()
		return st.Role == core.RoleLeader && st.Term == 2 && st.Commit >= 2 && f.Status().Lead == lateX
	}) {
		return core.MessageValue{}, false
	}
	n.tick(200*time.Millisecond, nil)

	// 2. F forwards a command to its leader X, at term 2. The frame is
	//    delayed in the network (it is delivered at step 7).
	data := []byte("c=1")
	c := core.MessageValue{
		Version: 1, Group: 1, Kind: core.MsgForward, From: lateF, To: lateX, Term: f.Status().Term, Context: 77,
		Entries: []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(data), Data: data}},
	}
	if c.Term != 2 {
		n.t.Fatalf("seed %d: setup: F at term %d", seed, c.Term)
	}

	// 3. X is cut off; G wins term 3 with F.
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.From == lateX || m.To == lateX || (m.Kind == core.MsgPreVote && m.From == lateF) {
			return lateDrop
		}
		return lateDeliver
	}
	return c, n.tick(10*time.Second, func() bool {
		st := g.Status()
		return st.Role == core.RoleLeader && st.Term == 3 && st.Commit >= 3 && f.Status().Lead == lateG
	})
}

// lateRace plays steps 4 to 8 of the review's schedules, after G won term 3
// with F while X, which led term 2, was cut off: X catches up; G's lease is
// renewed on F's acknowledgement while X campaigns for term 4; c, the
// message of term 2 held since step 2, reaches its receiver; then X's
// delayed vote request of term 4 reaches F. It reports whether the
// schedule was reached and the lease violation it found, if any.
func lateRace(n *lateNet, c core.MessageValue) (bool, string) {
	g, f, x := n.nodes[lateG], n.nodes[lateF], n.nodes[lateX]

	// 4. X hears G again (F and X still cut from each other) and catches up.
	n.policy = func(m core.MessageValue) lateVerdict {
		if (m.From == lateF && m.To == lateX) || (m.From == lateX && m.To == lateF) || (m.Kind == core.MsgPreVote && m.From == lateF) {
			return lateDrop
		}
		return lateDeliver
	}
	if !n.tick(3*time.Second, func() bool {
		st := x.Status()
		return st.Term == 3 && st.Lead == lateG && st.Last == g.Status().Last
	}) {
		return false, ""
	}
	n.tick(300*time.Millisecond, nil)
	if g.Status().Role != core.RoleLeader || g.Status().Term != 3 {
		return false, ""
	}

	// 5. G's round R1 reaches F and X; then G is cut off from both, F's
	//    acknowledgement of R1 is delayed 880 ms, and X's vote request to F
	//    is delayed. F's vote window, counted from R1, ends 1 s after R1;
	//    G's CheckQuorum, counted from F's acknowledgement, ends at R1 +
	//    1.88 s: in between, F grants X's PreVote while G still leads.
	var th time.Time
	var r1 uint64
	n.policy = func(m core.MessageValue) lateVerdict {
		switch {
		case m.Kind == core.MsgHeartbeat && m.From == lateG && th.IsZero():
			th, r1 = n.now, m.Context
			return lateDeliver
		case th.IsZero():
			if (m.From == lateF && m.To == lateX) || (m.From == lateX && m.To == lateF) || (m.Kind == core.MsgPreVote && m.From == lateF) {
				return lateDrop
			}
			return lateDeliver
		case m.Kind == core.MsgHeartbeat && m.From == lateG && m.Context == r1:
			return lateDeliver
		case m.From == lateG, m.From == lateX && m.To == lateG:
			return lateDrop
		case m.From == lateF && m.To == lateG:
			return lateHold
		case m.Kind == core.MsgPreVote && m.From == lateF:
			return lateDrop
		case m.Kind == core.MsgVote && m.From == lateX && m.To == lateF:
			return lateHold
		default:
			return lateDeliver
		}
	}
	var candidateAt time.Time
	ok := n.tick(3*time.Second, func() bool {
		if !th.IsZero() && !n.now.Before(th.Add(880*time.Millisecond)) {
			n.deliver(n.take(func(m core.MessageValue) bool { return m.From == lateF && m.To == lateG })...)
		}
		if g.Status().Role != core.RoleLeader {
			return true
		}
		if st := x.Status(); st.Role == core.RoleCandidate && st.Term == 4 {
			candidateAt = n.now
			return true
		}
		return false
	})
	if !ok || candidateAt.IsZero() || g.Status().Role != core.RoleLeader {
		return false, ""
	}

	// 6. G reaches F again: its next round renews the lease on F's
	//    acknowledgement. F follows G at term 3 and has just heard it.
	n.policy = func(m core.MessageValue) lateVerdict {
		switch {
		case m.From == lateG && m.To == lateX, m.From == lateX && m.To == lateG:
			return lateDrop
		case m.Kind == core.MsgVote && m.From == lateX && m.To == lateF:
			return lateHold
		case m.Kind == core.MsgPreVote && m.From == lateF:
			return lateDrop
		default:
			return lateDeliver
		}
	}
	if !n.tick(200*time.Millisecond, func() bool {
		return g.Status().Role == core.RoleLeader && g.LeaseValid(n.now) && g.leaseStart.After(candidateAt)
	}) {
		return false, ""
	}
	renewed := n.now
	if st := f.Status(); st.Term != 3 || st.Lead != lateG || n.now.Sub(f.lastLeaderContact) >= f.voteWindow {
		return false, ""
	}

	// 7. The delayed message of term 2 reaches its receiver.
	n.deliver(c)
	fTerm := f.Status().Term

	// 8. X's delayed vote request of term 4 reaches F.
	n.deliver(n.take(func(m core.MessageValue) bool { return m.Kind == core.MsgVote && m.From == lateX && m.To == lateF })...)
	if x.Status().Role != core.RoleLeader {
		return true, ""
	}
	w := []byte("w=2")
	pos, code := x.Propose(n.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(w), Data: w}})
	if code != 0 {
		return true, ""
	}
	n.pump()
	gs, xs := g.Status(), x.Status()
	if gs.Role == core.RoleLeader && g.LeaseValid(n.now) && xs.Commit >= pos[0].Index && gs.Commit < pos[0].Index {
		return true, fmt.Sprintf(
			"F followed live leader G (term 3, last contact %v before), adopted term %d from X's answer to its delayed term-2 message (kind %#x), "+
				"and granted X's term-4 vote; X leads term 4 and committed W at %+v, while G still leads term 3 with a valid lease "+
				"(renewed %v, now %v) at commit %d: a lease read on G misses W (D9 (b))",
			n.now.Sub(f.lastLeaderContact), fTerm, uint8(c.Kind), pos[0], renewed.Sub(t0), n.now.Sub(t0), gs.Commit)
	}
	return true, ""
}

// lateForwardSeeds plays seeds 1 to 64 and returns how many reached the
// schedule, how many of those violated the lease, and the first violation.
func lateForwardSeeds(t *testing.T, restamp bool, before ...core.NodeID) (int, int, string) {
	reached, violated, first := 0, 0, ""
	for seed := uint64(1); seed <= 64; seed++ {
		ok, violation := lateForwardSchedule(t, seed, restamp, before...)
		if !ok {
			continue
		}
		reached++
		if violation != "" {
			violated++
			if first == "" {
				first = fmt.Sprintf("seed %d: %s", seed, violation)
			}
		}
	}
	return reached, violated, first
}

// TestALeaseHoldsAcrossALateForwardAnswer is D9 (b) on the review's
// schedule of #1457 (D1): every seed that reaches it keeps the lease, the
// answer kept at its forward's term, read by this release or by F of a
// previous release, which takes the term an answer carries: the answer's
// term, kept by handleForward, is all that protects that reader (the
// safety review of #1472, §5.2). So does every seed when the answer is
// re-stamped at its sender's term but read by this release, which never
// takes an answer's later term (#1472). Re-stamped and read by a release
// before #1472, the step function before both fixes, every such seed
// violates it: the schedule is the one that catches it.
func TestALeaseHoldsAcrossALateForwardAnswer(t *testing.T) {
	reached, violated, first := lateForwardSeeds(t, false)
	if reached == 0 {
		t.Fatal("no seed reached the schedule: it no longer plays the late forward's answer")
	}
	if violated > 0 {
		t.Fatalf("%d of %d seeds that reached the schedule violated the lease; first: %s", violated, reached, first)
	}
	reachedF, violatedF, firstF := lateForwardSeeds(t, false, lateF)
	if reachedF == 0 || violatedF > 0 {
		t.Fatalf("read by F of a previous release, %d of %d seeds violated the lease; first: %s", violatedF, reachedF, firstF)
	}
	reachedRd, violatedRd, firstRd := lateForwardSeeds(t, true)
	if violatedRd > 0 {
		t.Fatalf("re-stamped and read by this release, %d of %d seeds violated the lease; first: %s", violatedRd, reachedRd, firstRd)
	}
	reachedRe, violatedRe, firstRe := lateForwardSeeds(t, true, lateG, lateF, lateX)
	if violatedRe == 0 {
		t.Fatalf("the answer re-stamped with its sender's term, read by a release before #1472, violated no lease over %d seeds that reached the schedule: it catches nothing (is the late forward still answered?)", reachedRe)
	}
	t.Logf("%d seeds reached the schedule, no violation, none of %d read by F of a previous release; re-stamped, none of %d read by this release, %d of %d read by a previous release violated it, first: %s",
		reached, reachedF, reachedRd, violatedRe, reachedRe, firstRe)
}
