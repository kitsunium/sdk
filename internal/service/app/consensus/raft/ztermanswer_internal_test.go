package raft

import (
	"errors"
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// An answer stays at the term of the request it answers, and its receiver
// never takes a later one (#1472). The refusal R1-18 sends to an append or
// a heartbeat of an older term carried the refuser's term: read by a node
// that had led that older term and since followed a live leader, it raised
// that node's term with no vote and no leader, past D5's live-leader
// refusal, and the node granted the next vote of that term while the
// leader it had just acknowledged still served lease reads (D9 (b)). The
// mutation type is full, so each check takes its mutation as an argument:
// nodes run as a release before #1472 (termFromAnswers), which answer at
// their own term and take the term an answer carries.

// laterAnswer is one answer of a later term than its receiver's, as a
// release before #1472 sent it.
type laterAnswer struct {
	name string
	m    core.MessageValue
}

// laterAnswers are the answers X (node 3), at term 4, sends F (node 2) to
// traffic F sent at an older term: every kind that answers a request.
func laterAnswers() []laterAnswer {
	at4 := func(kind core.MessageKind) core.MessageValue { return msg(kind, 3, 2, 4) }
	app := at4(core.MsgAppResp)
	app.Reject, app.Index = true, 1
	pre := at4(core.MsgPreVoteResp)
	pre.Reject = true
	vote := at4(core.MsgVoteResp)
	vote.Reject = true
	read := at4(core.MsgReadIndexResp)
	read.Context, read.Code = 5, codeNotLeader
	fwd := at4(core.MsgForwardResp)
	fwd.Context, fwd.Reject, fwd.Code = 9, true, codeNotLeader
	admin := at4(core.MsgForwardAdminResp)
	admin.Context, admin.Reject, admin.Code = 10, true, codeNotLeader
	return []laterAnswer{
		{"the refusal of an append of an older term (R1-18)", app},
		{"a PreVote's late refusal", pre},
		{"a vote's answer", vote},
		{"a heartbeat's answer", at4(core.MsgHeartbeatResp)},
		{"a deposed leader's read refusal", read},
		{"a forward's answer", fwd},
		{"an administrative forward's answer", admin},
	}
}

// followerOfG is F (node 2), which led term 2 over one entry and follows G
// (node 1) at term 3, having just heard it; before runs it as a release
// before #1472.
func followerOfG(now time.Time, before bool) (*Raft, error) {
	f := newTest(2, testState{term: 2, commit: 1, ents: chainEntries(0, 1, 1)}, 0, false)
	f.cfg.termFromAnswers = before
	hb := msg(core.MsgHeartbeat, 1, 2, 3)
	hb.Commit = 1
	f.Step(now, hb)
	if st := f.Status(); st.Term != 3 || st.Lead != 1 {
		return nil, fmt.Errorf("setup: F at term %d following %d, want term 3 following G", st.Term, st.Lead)
	}
	f.Ready()
	return f, nil
}

// checkAnAnswerNeverRaisesItsReceiversTerm hands F each later answer, then
// X's vote request of term 4. It returns an error when an answer moves F
// past term 3 or off G, or F grants that vote; and when a pre-candidate,
// refused by X at term 4, takes another term than the one it asked for.
func checkAnAnswerNeverRaisesItsReceiversTerm(before bool) error {
	now := t0.Add(5 * time.Second)
	var found []error
	for _, a := range laterAnswers() {
		f, err := followerOfG(now, before)
		if err != nil {
			return err
		}
		f.Step(now, a.m)
		vote := msg(core.MsgVote, 3, 2, 4)
		vote.Index, vote.LogTerm = 1, 1
		f.Step(now, vote)
		switch st := f.Status(); {
		case granted(f.Ready(), core.MsgVoteResp):
			found = append(found, fmt.Errorf("%s at term 4: F, which had just heard live leader G at term 3, granted X's vote of term 4", a.name))
		case st.Term != 3 || st.Lead != 1:
			found = append(found, fmt.Errorf("%s at term 4 moved F to term %d following %d", a.name, st.Term, st.Lead))
		}
	}
	p := newTest(2, testState{term: 2, commit: 1, ents: chainEntries(0, 1, 1)}, 0, false)
	p.cfg.termFromAnswers = before
	p.Tick(t0.Add(3 * time.Second))
	if p.role != core.RolePreCandidate {
		return fmt.Errorf("setup: role %v, want a pre-candidate", p.role)
	}
	pre := msg(core.MsgPreVoteResp, 3, 2, 4)
	pre.Reject = true
	p.Step(t0.Add(3*time.Second), pre)
	if p.term != 3 {
		found = append(found, fmt.Errorf("a pre-candidate of term 3, refused at term 4, moved to term %d", p.term))
	}
	return errors.Join(found...)
}

// TestAnAnswerNeverRaisesItsReceiversTerm: no answer of a later term moves
// a follower of a live leader, nor makes it grant a vote of that term, and
// a refused pre-candidate takes the term it asked for. Run as a release
// before #1472, the follower takes every one of them and grants the vote.
func TestAnAnswerNeverRaisesItsReceiversTerm(t *testing.T) {
	if err := checkAnAnswerNeverRaisesItsReceiversTerm(false); err != nil {
		t.Fatal(err)
	}
	err := checkAnAnswerNeverRaisesItsReceiversTerm(true)
	if err == nil {
		t.Fatal("a release before #1472, which takes the term an answer carries, was not caught")
	}
	t.Logf("a release before #1472 is caught:\n%v", err)
}

// answersOf returns what r sends, in order, AfterSync included.
func answersOf(rd ReadyValue) []core.MessageValue {
	return append(append([]core.MessageValue{}, rd.Messages...), rd.AfterSync...)
}

// checkAnOlderTermIsAnsweredAtItsTerm has X, at term 4, answer an append and
// a heartbeat of term 2 and a PreVote of term 3, and a leader of term 2
// deposed by a heartbeat of term 4 refuse the read a follower asked it at
// term 2. It returns an error when an answer is at another term than its
// request's, or an append's refusal does not say NOT_LEADER.
func checkAnOlderTermIsAnsweredAtItsTerm(before bool) error {
	now := t0.Add(5 * time.Second)
	x := newTest(3, testState{term: 4}, 0, false)
	x.cfg.termFromAnswers = before
	app := msg(core.MsgApp, 2, 3, 2)
	x.Step(now, app)
	x.Step(now, msg(core.MsgHeartbeat, 2, 3, 2))
	x.Step(now, msg(core.MsgPreVote, 2, 3, 3))
	var found []error
	n := 0
	for _, m := range answersOf(x.Ready()) {
		switch {
		case m.Kind == core.MsgAppResp && (m.Term != 2 || !m.Reject || m.Code != codeNotLeader):
			found = append(found, fmt.Errorf("an append of term 2 answered %+v, want a refusal at term 2 saying NOT_LEADER", m))
		case m.Kind == core.MsgPreVoteResp && (m.Term != 3 || !m.Reject):
			found = append(found, fmt.Errorf("a PreVote of term 3 answered %+v, want a refusal at term 3", m))
		}
		n++
	}
	if n != 3 {
		found = append(found, fmt.Errorf("X sent %d answers to three requests of older terms", n))
	}
	l := leaderOf(2, 0, nil, 0, false)
	l.cfg.termFromAnswers = before
	read := msg(core.MsgReadIndex, 2, 1, 2)
	read.Context = 7
	l.Step(l.now, read)
	l.Ready()
	l.Step(l.now, msg(core.MsgHeartbeat, 3, 1, 4))
	refused := false
	for _, m := range answersOf(l.Ready()) {
		if m.Kind == core.MsgReadIndexResp && m.Context == 7 {
			refused = true
			if m.Term != 2 || m.Code != codeNotLeader {
				found = append(found, fmt.Errorf("a leader deposed at term 4 refused a read asked at term 2 with %+v, want NOT_LEADER at term 2", m))
			}
		}
	}
	if !refused {
		found = append(found, errors.New("a deposed leader never refused the read it held"))
	}
	return errors.Join(found...)
}

// TestAnOlderTermIsAnsweredAtItsTerm: R1-18's refusal, a PreVote's and a
// deposed leader's read refusal stay at their request's term. Run as a
// release before #1472, each is at the answering node's later term.
func TestAnOlderTermIsAnsweredAtItsTerm(t *testing.T) {
	if err := checkAnOlderTermIsAnsweredAtItsTerm(false); err != nil {
		t.Fatal(err)
	}
	err := checkAnOlderTermIsAnsweredAtItsTerm(true)
	if err == nil {
		t.Fatal("a release before #1472, which answers at its own term, was not caught")
	}
	t.Logf("a release before #1472 is caught:\n%v", err)
}

// checkAStaleLeaderStepsDownAtItsOwnTerm has a leader of term 2 heartbeat
// X, at term 4, and read X's refusal. xBefore and lBefore run X and the
// leader as a release before #1472. It returns an error when the leader
// keeps leading, takes X's term, or still holds its lease.
func checkAStaleLeaderStepsDownAtItsOwnTerm(xBefore, lBefore bool) error {
	l := leaderOf(2, 0, nil, 0, true)
	l.cfg.termFromAnswers = lBefore
	x := newTest(3, testState{term: 4}, 0, false)
	x.cfg.termFromAnswers = xBefore
	now := l.now.Add(testTiming().HeartbeatInterval)
	l.Tick(now)
	var hb core.MessageValue
	for _, m := range answersOf(l.Ready()) {
		if m.Kind == core.MsgHeartbeat && m.To == 3 {
			hb = m
		}
	}
	if hb.Term != 2 {
		return fmt.Errorf("setup: no heartbeat of term 2 to X: %+v", hb)
	}
	x.Step(now, hb)
	refusals := 0
	for _, m := range answersOf(x.Ready()) {
		if m.Kind == core.MsgAppResp && m.To == 1 {
			l.Step(now, m)
			refusals++
		}
	}
	st := l.Status()
	switch {
	case refusals != 1:
		return fmt.Errorf("X answered a heartbeat of term 2 with %d refusals", refusals)
	case st.Role == core.RoleLeader:
		return fmt.Errorf("the leader of term 2 kept leading on X's refusal (term %d)", st.Term)
	case st.Term != 2:
		return fmt.Errorf("the leader of term 2 took X's term %d from its refusal", st.Term)
	case st.Lead != 0 || l.LeaseValid(now):
		return fmt.Errorf("the leader stepped down following %d, lease valid %v", st.Lead, l.LeaseValid(now))
	}
	return nil
}

// TestAStaleLeaderStepsDownAtItsOwnTerm is R1-18 under #1472: a leader
// refused by a member at a later term steps down on that refusal at its own
// term, the refusal of this release or one of a release before #1472 at
// the member's term. A leader of a release before #1472 reads this
// release's refusal as one of its log and keeps leading (the cost of a
// mixed cluster, which the package guide states), and one that meets a
// refusal of its own release takes the member's term: the check catches
// both.
func TestAStaleLeaderStepsDownAtItsOwnTerm(t *testing.T) {
	for _, xBefore := range []bool{false, true} {
		if err := checkAStaleLeaderStepsDownAtItsOwnTerm(xBefore, false); err != nil {
			t.Fatalf("X before #1472 %v: %v", xBefore, err)
		}
	}
	for _, xBefore := range []bool{false, true} {
		err := checkAStaleLeaderStepsDownAtItsOwnTerm(xBefore, true)
		if err == nil {
			t.Fatalf("a leader before #1472, X before #1472 %v, was not caught", xBefore)
		}
		t.Logf("a leader before #1472, X before #1472 %v: %v", xBefore, err)
	}
}

// stuckNode is node id at term, a voter of members 1, 2 and 3 with the
// lease on, over the founding snapshot alone: a node stuck at a later term
// than its cluster's, which campaigned and lost while cut off (R1-18).
func stuckNode(id core.NodeID, seed, term uint64) *Raft {
	tm := testTiming()
	cfg := &Config{
		ID: id, Group: 1, Seed: seed, Timing: tm, MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, Lease: true, CanVote: true, MaxChainRefusals: 3,
	}
	return NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: term, Commit: 1},
		Snapshot: core.SnapshotMetaValue{
			Position: core.PositionValue{Term: 1, Index: 1}, Membership: testConf(1, 2, 3), Timing: tm,
		},
		Now: t0,
	})
}

// stuckResult is what one seed of the stuck member's schedule measured.
type stuckResult struct {
	// down is the delay from the heal to the leader's step-down.
	down time.Duration
	// readmitted is the delay from the heal to X following a leader with
	// its log; terms is that leader's term.
	readmitted time.Duration
	terms      uint64
}

// stuckMemberSchedule plays R1-18's case on G, F and X under seed, X stuck at
// term 5: X is cut off while G and F elect a leader, of term 2 unless a
// split vote took one more, and commit a write; then the network heals. It returns what it measured, or an error
// when the leader keeps leading past a heartbeat interval and a round of
// ticks, steps down at another term than its own, or X does not follow a
// leader with its log within 30 s. before names the nodes run as a release
// before #1472.
func stuckMemberSchedule(t *testing.T, seed uint64, before ...core.NodeID) (stuckResult, error) {
	n := newLateNet(t, seed, before...)
	n.nodes[lateX] = stuckNode(lateX, seed, 5)
	for _, id := range before {
		n.nodes[id].cfg.termFromAnswers = true
	}
	x := n.nodes[lateX]
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.From == lateX || m.To == lateX {
			return lateDrop
		}
		return lateDeliver
	}
	var l *Raft
	if !n.tick(10*time.Second, func() bool {
		for _, id := range []core.NodeID{lateG, lateF} {
			if st := n.nodes[id].Status(); st.Role == core.RoleLeader && st.Term < 5 && st.Last >= 2 && st.Commit == st.Last {
				l = n.nodes[id]
			}
		}
		return l != nil
	}) {
		return stuckResult{}, errors.New("setup: G and F elected no leader below term 5")
	}
	led := l.Status().Term
	w := []byte("w=1")
	pos, code := l.Propose(n.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(w), Data: w}})
	if code != 0 || !n.tick(time.Second, func() bool { return l.Status().Commit >= pos[0].Index }) {
		return stuckResult{}, fmt.Errorf("setup: the write was not committed (%v)", code)
	}
	if st := x.Status(); st.Term != 5 || st.Last != 1 {
		return stuckResult{}, fmt.Errorf("setup: X at term %d, last %d", st.Term, st.Last)
	}
	n.policy = func(core.MessageValue) lateVerdict { return lateDeliver }
	healed := n.now
	var res stuckResult
	var downTerm uint64
	n.tick(2*time.Second, func() bool {
		if st := l.Status(); st.Role != core.RoleLeader {
			res.down, downTerm = n.now.Sub(healed), st.Term
			return true
		}
		return false
	})
	switch bound := testTiming().HeartbeatInterval + 40*time.Millisecond; {
	case res.down == 0:
		return res, fmt.Errorf("the leader of term %d kept leading for 2 s after X came back at term 5 (term %d)", led, l.Status().Term)
	case downTerm != led:
		return res, fmt.Errorf("the leader of term %d stepped down at term %d", led, downTerm)
	case res.down > bound:
		return res, fmt.Errorf("the leader stepped down %v after the heal, past %v", res.down, bound)
	}
	if !n.tick(30*time.Second, func() bool {
		xs := x.Status()
		if xs.Lead == 0 {
			return false
		}
		ls := n.nodes[xs.Lead].Status()
		if ls.Role == core.RoleLeader && ls.Term == xs.Term && xs.Last == ls.Last && xs.Commit == ls.Commit && xs.Commit >= pos[0].Index {
			res.readmitted, res.terms = n.now.Sub(healed), ls.Term
			return true
		}
		return false
	}) {
		xs := x.Status()
		return res, fmt.Errorf("X never followed a leader with its log within 30 s: X at term %d following %d, last %d", xs.Term, xs.Lead, xs.Last)
	}
	return res, nil
}

// TestAStuckMemberIsReadmittedAfterItsLeaderStepsDown is R1-18 end to end on
// three step functions: the leader G and F elected while X was cut off steps
// down at its own term within a heartbeat interval of X's return at term 5,
// and a PreVote round a term, up to X's, makes X follow a leader with the
// cluster's log. It holds
// with X answering as a release before #1472 too, at its own term.
func TestAStuckMemberIsReadmittedAfterItsLeaderStepsDown(t *testing.T) {
	for _, before := range [][]core.NodeID{nil, {lateX}} {
		var worstDown, worstBack time.Duration
		for seed := uint64(1); seed <= 16; seed++ {
			res, err := stuckMemberSchedule(t, seed, before...)
			if err != nil {
				t.Fatalf("seed %d, run as a release before #1472 %v: %v", seed, before, err)
			}
			worstDown, worstBack = max(worstDown, res.down), max(worstBack, res.readmitted)
		}
		t.Logf("run as a release before #1472 %v: step-down at most %v after the heal, X following a leader with its log at most %v after",
			before, worstDown, worstBack)
	}
}

// lateAppendSetup plays steps 1 to 3 of D0's schedule, the review of #1457:
// F leads term 2 and its heartbeat to X is held in the network; F is cut
// off until CheckQuorum steps it down; G wins term 3 with F. It returns
// F's heartbeat of term 2 and whether seed reached G's leadership.
func lateAppendSetup(n *lateNet) (core.MessageValue, bool) {
	g, f, x := n.nodes[lateG], n.nodes[lateF], n.nodes[lateX]
	var c core.MessageValue

	// 1. F wins term 2: the PreVotes of G and X are lost.
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.Kind == core.MsgPreVote && m.From != lateF {
			return lateDrop
		}
		return lateDeliver
	}
	if !n.tick(5*time.Second, func() bool {
		st := f.Status()
		return st.Role == core.RoleLeader && st.Term == 2 && st.Commit >= 2 && x.Status().Lead == lateF && g.Status().Lead == lateF
	}) {
		return c, false
	}
	n.tick(200*time.Millisecond, nil)

	// 2. F's next heartbeat to X, of term 2, is held in the network (it is
	//    delivered at step 7); nothing else reaches X.
	held := false
	n.policy = func(m core.MessageValue) lateVerdict {
		switch {
		case !held && m.Kind == core.MsgHeartbeat && m.From == lateF && m.To == lateX:
			held, c = true, m
			return lateDrop
		case m.From == lateX || m.To == lateX:
			return lateDrop
		default:
			return lateDeliver
		}
	}
	if !n.tick(time.Second, func() bool { return held }) || c.Term != 2 {
		return c, false
	}

	// 3. F is cut off from everyone until CheckQuorum steps it down; then
	//    G and F hear each other again, X still cut off, and G wins term 3
	//    with F.
	n.policy = func(core.MessageValue) lateVerdict { return lateDrop }
	n.tick(1500*time.Millisecond, nil)
	if f.Status().Role == core.RoleLeader {
		return c, false
	}
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

// lateAppendSeeds plays D0's schedule over seeds 1 to 64 and returns how
// many reached it, how many of those violated the lease, and the first
// violation; before names the nodes run as a release before #1472.
func lateAppendSeeds(t *testing.T, before ...core.NodeID) (int, int, string) {
	reached, violated, first := 0, 0, ""
	for seed := uint64(1); seed <= 64; seed++ {
		n := newLateNet(t, seed, before...)
		c, ok := lateAppendSetup(n)
		if !ok {
			continue
		}
		ok, violation := lateRace(n, c)
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

// TestALeaseHoldsAcrossALateAppendAnswer is D9 (b) on D0's schedule, the
// review of #1457 (#1472): F led term 2 and its heartbeat to X is delayed;
// F follows G at term 3 and has just acknowledged G's lease when X, a
// candidate of term 4, refuses that heartbeat; then X's delayed vote request
// of term 4 reaches F. Every seed that reaches the schedule keeps the lease:
// the refusal is at term 2, which F drops. So does every seed when X, or F,
// alone runs as a release before #1472: F never takes the refusal's term 4,
// or X refuses at term 2. Run all as a release before #1472, the step
// function before the fix, every such seed violates it, 48 of 48.
func TestALeaseHoldsAcrossALateAppendAnswer(t *testing.T) {
	for _, before := range [][]core.NodeID{nil, {lateX}, {lateF}} {
		reached, violated, first := lateAppendSeeds(t, before...)
		if reached == 0 {
			t.Fatalf("run as a release before #1472 %v: no seed reached the schedule: it no longer plays the late refusal", before)
		}
		if violated > 0 {
			t.Fatalf("run as a release before #1472 %v: %d of %d seeds that reached the schedule violated the lease; first: %s", before, violated, reached, first)
		}
		t.Logf("run as a release before #1472 %v: %d seeds reached the schedule, no violation", before, reached)
	}
	reached, violated, first := lateAppendSeeds(t, lateG, lateF, lateX)
	if violated == 0 {
		t.Fatalf("run all as a release before #1472, %d seeds reached the schedule and none violated the lease: it catches nothing", reached)
	}
	t.Logf("run all as a release before #1472, %d of %d seeds violated the lease; first: %s", violated, reached, first)
}

// TestLeaseReadsAreLinearizableUnderFaultsInAMixedCluster runs the client
// workload of TestLinearizableUnderFaults with its reads served under the
// lease, which D0 broke, node 3 of a previous release on odd seeds and
// nodes 1 and 2 on even ones (speaks), which answer older-term traffic and
// read a later term's answers as every release before #1472: a node of
// this release that reads such an answer never takes its term, and one of a
// previous release that reads this release's refusal finds it at its
// request's term. Eight seeds, one mix each: the test sorts after every
// seed suite but shares a Bazel shard with three seed parts, and both
// mixes passed 400 seeds swept by CONSENSUS_SIM_SEED.
func TestLeaseReadsAreLinearizableUnderFaultsInAMixedCluster(t *testing.T) {
	eachSeed(t, seeds(t, 8), func(t *testing.T, seed uint64) {
		speaks := map[core.NodeID]uint32{3: Protocol3}
		if seed%2 == 0 {
			speaks = map[core.NodeID]uint32{1: Protocol3, 2: Protocol3}
		}
		opt := simOptions{seed: seed, drop: 0.03, crashRate: 0.3, lease: true, speaks: speaks}
		if err := linearizableRunWith(opt); err != nil {
			t.Fatalf("previous release %v: %v", speaks, err)
		}
	})
}
