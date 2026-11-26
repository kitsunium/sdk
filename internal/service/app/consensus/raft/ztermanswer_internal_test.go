package raft

import (
	"errors"
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// An answer stays at the term of the request it answers, and of the nodes
// that may read it only the two that hold no lease promise take a later
// term from one: the leader refused its own append or heartbeat, and the
// pre-candidate refused its own PreVote, each at the refuser's term, this
// release's in LogTerm (#1472). The refusal R1-18 sends to an append or a
// heartbeat of an older term carried the refuser's term: read by a node
// that had led that older term and since followed a live leader, it raised
// that node's term with no vote and no leader, past D5's live-leader
// refusal, and the node granted the next vote of that term while the
// leader it had just acknowledged still served lease reads (D9 (b)). The
// mutation type is full, so each check takes its mutation as an argument:
// nodes run as a release before #1472 (termFromAnswers), which answer at
// their own term, take the term an answer carries and announce protocol 4
// at most.

// errSetup marks a check whose fixture never reached the state it judges:
// a control that returns it caught nothing, whatever release it ran as.
var errSetup = errors.New("setup")

// releaseMode is one release a control runs its nodes as: this one, which
// the check must pass, or one before #1472 (termFromAnswers), which it must
// catch.
type releaseMode struct {
	name   string
	before bool
}

// releaseModes are the two releases every control runs.
func releaseModes() []releaseMode {
	return []releaseMode{{name: "this release"}, {name: "a release before #1472", before: true}}
}

// judgeControl fails t when err is a setup failure, when a run that must
// pass returns an error, or when a run that must be caught returns none;
// missed names what a run that must be caught let through.
func judgeControl(t *testing.T, caught bool, err error, missed string) {
	t.Helper()
	switch {
	case errors.Is(err, errSetup):
		t.Fatalf("the fixture never reached the state it judges: %v", err)
	case !caught && err != nil:
		t.Fatal(err)
	case caught && err == nil:
		t.Fatalf("%s was not caught", missed)
	case caught:
		t.Logf("caught:\n%v", err)
	}
}

// laterMessage is one message of a later term than its receiver's that is
// no request of that term (termRequestKind).
type laterMessage struct {
	name string
	m    core.MessageValue
}

// laterMessages are the messages X (node 3), at term 4, may send F (node 2)
// that no request of term 4 is: every kind that answers a request, as a
// release before #1472 answered traffic F sent at an older term, a read and
// the forwards, which no release sends past its receiver's term, and a kind
// no release names.
func laterMessages() []laterMessage {
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
	ask := at4(core.MsgReadIndex)
	ask.Context = 6
	forward := forwardCmd(4, 11, 8)
	forward.From, forward.To = 3, 2
	return []laterMessage{
		{"the refusal of an append of an older term (R1-18)", app},
		{"a PreVote's late refusal", pre},
		{"a vote's answer", vote},
		{"a heartbeat's answer", at4(core.MsgHeartbeatResp)},
		{"a deposed leader's read refusal", read},
		{"a forward's answer", fwd},
		{"an administrative forward's answer", admin},
		{"a read", ask},
		{"a forward", forward},
		{"an administrative forward", at4(core.MsgForwardAdmin)},
		{"a kind no release names", at4(core.MessageKind(0x3F))},
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
		return nil, fmt.Errorf("%w: F at term %d following %d, want term 3 following G", errSetup, st.Term, st.Lead)
	}
	f.Ready()
	return f, nil
}

// checkALaterMessageNeverRaisesAFollowersTerm hands F each later message,
// then X's vote request of term 4. It returns an error when one moves F past
// term 3 or off G, or F grants that vote.
func checkALaterMessageNeverRaisesAFollowersTerm(before bool) error {
	now := t0.Add(5 * time.Second)
	var found []error
	for _, a := range laterMessages() {
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
	return errors.Join(found...)
}

// TestOnlyARequestRaisesAFollowersTerm: no message of a later term but a
// request of that term moves a follower of a live leader, nor makes it
// grant a vote of that term: no answer, which stays at its request's term,
// no read or forward, which a node sends its own leader, and no kind the
// list of termRequestKind does not name. Run as a release before #1472,
// the follower takes every one of them and grants the vote.
func TestOnlyARequestRaisesAFollowersTerm(t *testing.T) {
	runCase := func(t *testing.T, c releaseMode) {
		judgeControl(t, c.before, checkALaterMessageNeverRaisesAFollowersTerm(c.before),
			"a release before #1472, which takes the term any message carries,")
	}
	for _, c := range releaseModes() {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// checkARefusedPreCandidateTakesTheRefusersTerm has a pre-candidate of term
// 2, asking for term 3, read X's refusal: this release's, at the term it
// asked for with X's term 4 in LogTerm, and a previous release's, at X's
// term. It returns an error when the pre-candidate does not follow nobody
// at X's term.
func checkARefusedPreCandidateTakesTheRefusersTerm(before bool) error {
	var found []error
	for _, c := range []struct {
		name          string
		term, logTerm uint64
	}{
		{"this release's refusal, at the term it asked for", 3, 4},
		{"a previous release's refusal, at the refuser's term", 4, 0},
	} {
		p := newTest(2, testState{term: 2, commit: 1, ents: chainEntries(0, 1, 1)}, 0, false)
		p.cfg.termFromAnswers = before
		now := t0.Add(3 * time.Second)
		p.Tick(now)
		if p.role != core.RolePreCandidate {
			return fmt.Errorf("%w: role %v, want a pre-candidate", errSetup, p.role)
		}
		pre := msg(core.MsgPreVoteResp, 3, 2, c.term)
		pre.Reject, pre.LogTerm = true, c.logTerm
		p.Step(now, pre)
		if st := p.Status(); st.Term != 4 || st.Role != core.RoleFollower || st.Lead != 0 {
			found = append(found, fmt.Errorf("%s: the pre-candidate of term 2, refused by X at term 4, is a %v at term %d following %d",
				c.name, st.Role, st.Term, st.Lead))
		}
	}
	return errors.Join(found...)
}

// TestARefusedPreCandidateTakesTheRefusersTerm: a pre-candidate is outside
// its vote window and holds no lease promise, so the refusal of its own
// PreVote, this release's or a previous one's, ends its campaign at the
// refuser's term, and the cluster reaches a member stuck ahead in one
// round (#1472). Run as a release before #1472, the pre-candidate takes
// the term the refusal is stamped with, the one it asked for.
func TestARefusedPreCandidateTakesTheRefusersTerm(t *testing.T) {
	runCase := func(t *testing.T, c releaseMode) {
		judgeControl(t, c.before, checkARefusedPreCandidateTakesTheRefusersTerm(c.before),
			"a release before #1472, which takes the term the refusal is stamped with,")
	}
	for _, c := range releaseModes() {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// answersOf returns what r sends, in order, AfterSync included.
func answersOf(rd ReadyValue) []core.MessageValue {
	return append(append([]core.MessageValue{}, rd.Messages...), rd.AfterSync...)
}

// checkAnOlderTermIsAnsweredAtItsTerm has X, at term 4, answer an append and
// a heartbeat of term 2 from a leader of protocol 5 and from one of a
// previous release, and a PreVote of term 3, and a leader of term 2 deposed
// by a heartbeat of term 4 refuse the read a follower asked it at term 2.
// It returns an error when a request is not answered by exactly one
// message, or its answer is not a refusal of the request's kind sent to its
// sender at the request's term, or at the next one for the leader of a
// previous release, with X's term in LogTerm, and saying NOT_LEADER for an
// append or a heartbeat.
func checkAnOlderTermIsAnsweredAtItsTerm(before bool) error {
	now := t0.Add(5 * time.Second)
	x := newTest(3, testState{term: 4}, 0, false)
	x.cfg.termFromAnswers = before
	p5, p4 := announcementOf(Protocol5), announcementOf(Protocol4)
	refusal := func(kind core.MessageKind, to core.NodeID, term uint64, code errs.Code) core.MessageValue {
		return core.MessageValue{Kind: kind, To: to, Term: term, LogTerm: 4, Reject: true, Code: code}
	}
	var found []error
	for _, c := range []struct {
		name string
		kind core.MessageKind
		from core.NodeID
		term uint64
		// flags is the announcement the request carries.
		flags core.MessageFlags
		want  core.MessageValue
	}{
		{"an append of term 2 from a leader of protocol 5", core.MsgApp, 2, 2, p5, refusal(core.MsgAppResp, 2, 2, codeNotLeader)},
		{"a heartbeat of term 2 from a leader of protocol 5", core.MsgHeartbeat, 2, 2, p5, refusal(core.MsgAppResp, 2, 2, codeNotLeader)},
		{"an append of term 2 from a leader of a previous release", core.MsgApp, 1, 2, p4, refusal(core.MsgAppResp, 1, 3, codeNotLeader)},
		{"a heartbeat of term 2 from a leader of a previous release", core.MsgHeartbeat, 1, 2, p4, refusal(core.MsgAppResp, 1, 3, codeNotLeader)},
		{"a PreVote of term 3", core.MsgPreVote, 2, 3, 0, refusal(core.MsgPreVoteResp, 2, 3, 0)},
	} {
		m := msg(c.kind, c.from, 3, c.term)
		m.Flags = c.flags
		x.Step(now, m)
		out := answersOf(x.Ready())
		if len(out) != 1 {
			found = append(found, fmt.Errorf("%s was answered with %d messages, want 1: %+v", c.name, len(out), out))
			continue
		}
		got, w := out[0], c.want
		if got.Kind != w.Kind || got.To != w.To || got.Term != w.Term || got.LogTerm != w.LogTerm || got.Reject != w.Reject || got.Code != w.Code {
			found = append(found, fmt.Errorf("%s was answered %+v, want a refusal of kind %#x to node %d at term %d, %d in LogTerm, code %v",
				c.name, got, uint8(w.Kind), w.To, w.Term, w.LogTerm, w.Code))
		}
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
// deposed leader's read refusal stay at their request's term, the first two
// with the refuser's term in LogTerm, but R1-18's to a leader of a previous
// release, which reads no LogTerm, at the term after its request's (O1 of
// the safety review of #1472's second version). Run as a release before
// #1472, each is at the answering node's later term.
func TestAnOlderTermIsAnsweredAtItsTerm(t *testing.T) {
	runCase := func(t *testing.T, c releaseMode) {
		judgeControl(t, c.before, checkAnOlderTermIsAnsweredAtItsTerm(c.before),
			"a release before #1472, which answers at its own term,")
	}
	for _, c := range releaseModes() {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAStaleLeaderStepsDownAtTheRefusersTerm is R1-18 under #1472: a leader
// of term 2, holding a live lease, heartbeats X, at term 4, and reads what X
// answers. X refuses a leader of protocol 5 at term 2, NOT_LEADER, its term
// in LogTerm, and the leader steps down at term 4 on the refusal of its own
// request, its lease dropped; a refusal of a previous release, at X's term, steps it down
// there too. A leader of a previous release, which reads no LogTerm, is
// refused at term 3, the term after its request's, and steps down there
// (O1 of the safety review of #1472's second version): refused at its own
// term, it counted the refusal as contact, and sent nothing, it kept
// leading while another member acknowledged it, X left out for as long
// (TestAFailedTransferLeavesNoMemberOut). Two nodes of a previous release
// step down as before #1472.
func TestAStaleLeaderStepsDownAtTheRefusersTerm(t *testing.T) {
	type tc struct {
		name             string
		lBefore, xBefore bool
		refusals         int
		leads            bool
		term             uint64
	}
	runCase := func(t *testing.T, c tc) {
		l := confirmedLeader(0)
		l.cfg.termFromAnswers = c.lBefore
		x := newTest(3, testState{term: 4}, 0, false)
		x.cfg.termFromAnswers = c.xBefore
		now := l.now.Add(testTiming().HeartbeatInterval)
		l.Tick(now)
		var hb core.MessageValue
		for _, m := range answersOf(l.Ready()) {
			if m.Kind == core.MsgHeartbeat && m.To == 3 {
				hb = m
			}
		}
		if hb.Term != 2 {
			t.Fatalf("setup: no heartbeat of term 2 to X: %+v", hb)
		}
		if !l.LeaseValid(now) {
			t.Fatal("setup: the leader of term 2 holds no live lease before X's refusal")
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
		case refusals != c.refusals:
			t.Fatalf("X answered a heartbeat of term 2 with %d refusals, want %d", refusals, c.refusals)
		case (st.Role == core.RoleLeader) != c.leads:
			t.Fatalf("the leader of term 2 is a %v at term %d, want leading %v", st.Role, st.Term, c.leads)
		case st.Term != c.term:
			t.Fatalf("the leader of term 2 is at term %d, want %d", st.Term, c.term)
		case !c.leads && (st.Lead != 0 || l.LeaseValid(now)):
			t.Fatalf("the leader stepped down following %d, lease valid %v", st.Lead, l.LeaseValid(now))
		}
	}
	for _, c := range []tc{
		{name: "this release", refusals: 1, term: 4},
		{name: "X of a previous release", xBefore: true, refusals: 1, term: 4},
		{name: "a leader of a previous release", lBefore: true, refusals: 1, term: 3},
		{name: "both of a previous release", lBefore: true, xBefore: true, refusals: 1, term: 4},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// checkANextTermRefusalRaisesOnlyItsLeader has X, at term 4, refuse a
// heartbeat of term 2 from node 1, of a previous release, which led term 2;
// restamp, when not nil, changes the refusal in flight, the mutation the
// check must catch. Node 1 reads it still leading term 2 with a live lease,
// then, in another run, as a follower of node 2 at term 3 that has just
// heard it, before X's vote request of term 4. It returns an error when the
// leader of term 2 does not step down past term 2 with its lease dropped,
// or the follower leaves term 3 or node 2, or grants the vote.
func checkANextTermRefusalRaisesOnlyItsLeader(restamp func(*core.MessageValue)) error {
	x := newTest(3, testState{term: 4}, 0, false)
	hb := msg(core.MsgHeartbeat, 1, 3, 2)
	hb.Flags = announcementOf(Protocol4)
	x.Step(t0, hb)
	var refusal core.MessageValue
	for _, m := range answersOf(x.Ready()) {
		if m.Kind == core.MsgAppResp && m.To == 1 {
			refusal = m
		}
	}
	if refusal.Kind != core.MsgAppResp {
		return fmt.Errorf("%w: X answered nothing to a heartbeat of term 2 from a leader of a previous release", errSetup)
	}
	if restamp != nil {
		restamp(&refusal)
	}
	var found []error
	l := confirmedLeader(0)
	l.cfg.termFromAnswers = true
	if !l.LeaseValid(l.now) {
		return fmt.Errorf("%w: the leader of term 2 holds no live lease before X's refusal", errSetup)
	}
	l.Step(l.now, refusal)
	if st := l.Status(); st.Role == core.RoleLeader || st.Term <= 2 || l.LeaseValid(l.now) {
		found = append(found, fmt.Errorf("the leader of term 2 read X's refusal (term %d) and is a %v at term %d, lease valid %v", refusal.Term, st.Role, st.Term, l.LeaseValid(l.now)))
	}
	now := t0.Add(5 * time.Second)
	f := newTest(1, testState{term: 2, commit: 1, ents: chainEntries(0, 1, 1)}, 0, false)
	f.cfg.termFromAnswers = true
	g := msg(core.MsgHeartbeat, 2, 1, 3)
	g.Commit = 1
	f.Step(now, g)
	f.Ready()
	f.Step(now, refusal)
	vote := msg(core.MsgVote, 3, 1, 4)
	vote.Index, vote.LogTerm = 1, 1
	f.Step(now, vote)
	switch st := f.Status(); {
	case granted(f.Ready(), core.MsgVoteResp):
		found = append(found, fmt.Errorf("a follower of a live leader at term 3 read X's refusal (term %d) and granted X's vote of term 4", refusal.Term))
	case st.Term != 3 || st.Lead != 2:
		found = append(found, fmt.Errorf("a follower of a live leader at term 3 read X's refusal (term %d) and is at term %d following %d", refusal.Term, st.Term, st.Lead))
	}
	return errors.Join(found...)
}

// TestARefusalAtTheNextTermRaisesOnlyTheLeaderOfTheRequest: X, at term 4,
// refuses a heartbeat of term 2 from a leader of a previous release at term
// 3, the term after its request's (O1 of the safety review of #1472's second
// version). Read by that node while it still leads term 2 under a live
// lease, the refusal steps it down, its lease dropped; read after it followed a live leader at
// term 3, the refusal is of that node's own term, a follower drops it, and
// the node refuses X's vote of term 4 (D5). Re-stamped with X's term in
// flight, as R1-18 answered before #1472, it raises the follower to term 4,
// with no vote and no leader, and the follower grants that vote (D0).
func TestARefusalAtTheNextTermRaisesOnlyTheLeaderOfTheRequest(t *testing.T) {
	type tc struct {
		name    string
		restamp func(*core.MessageValue)
		caught  bool
	}
	runCase := func(t *testing.T, c tc) {
		judgeControl(t, c.caught, checkANextTermRefusalRaisesOnlyItsLeader(c.restamp),
			"a refusal at the refuser's term, R1-18 before #1472,")
	}
	for _, c := range []tc{
		{name: "this release"},
		{name: "a refusal at the refuser's term", restamp: func(m *core.MessageValue) { m.Term = m.LogTerm }, caught: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestOnlyAMembersRefusalStepsALeaderDown: a leader of term 2 reads a
// NOT_LEADER refusal of its term carrying term 50 in LogTerm. From a member
// it steps down at term 50 (R1-18); from a node outside its configuration,
// a removed member whose frames still reach it, which was sent no request
// to refuse, it keeps leading at term 2 (O8 of the safety review of #1472's
// second version).
func TestOnlyAMembersRefusalStepsALeaderDown(t *testing.T) {
	type tc struct {
		name  string
		from  core.NodeID
		leads bool
		term  uint64
	}
	runCase := func(t *testing.T, c tc) {
		l := leaderOf(2, 0, nil, 0, false)
		m := msg(core.MsgAppResp, c.from, 1, 2)
		m.Reject, m.Code, m.LogTerm = true, codeNotLeader, 50
		l.Step(l.now, m)
		if st := l.Status(); (st.Role == core.RoleLeader) != c.leads || st.Term != c.term {
			t.Fatalf("a NOT_LEADER of term 2 carrying 50 from node %d: the leader is a %v at term %d, want leading %v at term %d",
				c.from, st.Role, st.Term, c.leads, c.term)
		}
	}
	for _, c := range []tc{
		{name: "a member", from: 3, term: 50},
		{name: "a node outside the configuration", from: 9, leads: true, term: 2},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestANotLeaderRefusalCarriesALaterTermThanItsRequest is the refuser's half
// of refusedForLaterTerm's reach, which steps a leader down only on a
// LogTerm past its term: every NOT_LEADER X sends, at terms 3 to 6, to an
// append or a heartbeat of every older term, carries in LogTerm a term past
// its request's. A leader of protocol 5 is refused at its request's term,
// LogTerm past it; a leader of a previous release at the term after, LogTerm
// no lower. So a leader of protocol 5 still at the request's term reads a
// LogTerm past its own, and a refusal whose LogTerm is the leader's term
// reaches it only as the refusal at the next term of a frame it sent as a
// leader of protocol 4 before its upgrade, which it drops (#1514;
// TestALeaderUpgradedTo5KeepsLeadingPastItsOwnOldFrame).
func TestANotLeaderRefusalCarriesALaterTermThanItsRequest(t *testing.T) {
	type tc struct {
		name         string
		xTerm, term  uint64
		kind         core.MessageKind
		protocol     uint32
		wantTermNext bool
	}
	runCase := func(t *testing.T, c tc) {
		x := newTest(3, testState{term: c.xTerm}, 0, false)
		m := msg(c.kind, 1, 3, c.term)
		m.Flags = announcementOf(c.protocol)
		x.Step(t0, m)
		out := answersOf(x.Ready())
		if len(out) != 1 {
			t.Fatalf("X answered with %d messages, want 1: %+v", len(out), out)
		}
		r, want := out[0], c.term
		if c.wantTermNext {
			want++
		}
		if r.Kind != core.MsgAppResp || !r.Reject || r.Code != codeNotLeader || r.Term != want || r.LogTerm != c.xTerm || r.LogTerm < r.Term || r.LogTerm <= c.term {
			t.Fatalf("X answered %+v, want NOT_LEADER at term %d, X's term %d in LogTerm, past the request's %d", r, want, c.xTerm, c.term)
		}
	}
	var cases []tc
	for xTerm := uint64(3); xTerm <= 6; xTerm++ {
		for term := uint64(1); term < xTerm; term++ {
			for _, kind := range []core.MessageKind{core.MsgApp, core.MsgHeartbeat} {
				cases = append(cases,
					tc{name: fmt.Sprintf("X at %d, kind %#x of term %d from a leader of protocol 5", xTerm, uint8(kind), term), xTerm: xTerm, term: term, kind: kind, protocol: Protocol5},
					tc{name: fmt.Sprintf("X at %d, kind %#x of term %d from a leader of a previous release", xTerm, uint8(kind), term), xTerm: xTerm, term: term, kind: kind, protocol: Protocol4, wantTermNext: true})
			}
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestALeaderPastARefusalsTermDropsIt is the reader's half: a leader of
// term 3 reads a member's refusal. Of an older term, it is dropped before
// stepLeader, whatever LogTerm it carries; at the leader's term, a refusal
// of the log, which says no NOT_LEADER, steps nobody down; a NOT_LEADER
// of the leader's term carrying a later term steps the leader down there,
// and one carrying the leader's own term, which refuses a frame of an older
// term, is dropped (#1514).
func TestALeaderPastARefusalsTermDropsIt(t *testing.T) {
	type tc struct {
		name          string
		term, logTerm uint64
		code          errs.Code
		leads         bool
		wantTerm      uint64
	}
	runCase := func(t *testing.T, c tc) {
		l := leaderOf(3, 0, nil, 0, false)
		m := msg(core.MsgAppResp, 2, 1, c.term)
		m.Reject, m.Code, m.LogTerm = true, c.code, c.logTerm
		l.Step(l.now, m)
		if st := l.Status(); (st.Role == core.RoleLeader) != c.leads || st.Term != c.wantTerm {
			t.Fatalf("the leader of term 3 read %+v and is a %v at term %d, want leading %v at term %d", m, st.Role, st.Term, c.leads, c.wantTerm)
		}
	}
	for _, c := range []tc{
		{name: "a NOT_LEADER of term 2 carrying 50", term: 2, logTerm: 50, code: codeNotLeader, leads: true, wantTerm: 3},
		{name: "a NOT_LEADER of term 2 carrying 3", term: 2, logTerm: 3, code: codeNotLeader, leads: true, wantTerm: 3},
		{name: "a refusal of the log at term 3, no NOT_LEADER", term: 3, leads: true, wantTerm: 3},
		{name: "a NOT_LEADER of term 3 carrying 5", term: 3, logTerm: 5, code: codeNotLeader, wantTerm: 5},
		{name: "a NOT_LEADER of term 3 carrying 3", term: 3, logTerm: 3, code: codeNotLeader, leads: true, wantTerm: 3},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// restartedLeader is node 1 started again over the durable state of old,
// the leader of term 2, and elected at term 3; before runs it as a release
// before #1472 still, a restart that did not upgrade it.
func restartedLeader(old *Raft, before bool) *Raft {
	cfg := old.cfg
	cfg.termFromAnswers = before
	r := NewRaft(&cfg, &StartState{
		HardState: core.HardState{Term: old.term, Vote: old.vote, Commit: old.log.committed},
		Snapshot:  core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries:   append([]core.EntryValue(nil), old.log.ents...),
		Now:       old.now,
	})
	r.now = old.now.Add(5 * time.Second)
	r.term, r.vote = old.term+1, 1
	r.becomeLeader()
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), r.term)
	r.Ready()
	return r
}

// TestALeaderUpgradedTo5KeepsLeadingPastItsOwnOldFrame is O4 of the safety
// review of #1472 (#1514): L leads term 2 as a release before #1472, which
// announces no protocol 5, and heartbeats X; the frame is held in the
// network while L is upgraded to protocol 5, restarted and elected at term
// 3. X, at term 3, then reads it and refuses it as a previous release's,
// at the term after its request's, 3, its own term 3 in LogTerm. That
// refusal is of no request L sent at term 3: L keeps leading term 3, its
// progress of X unmoved. It stepped down at its own term, its vote kept,
// one more election for nothing. A member past L's term still deposes it
// there, whatever release refuses; every other step of the upgrade, L or X
// still of the previous release, reads as before.
func TestALeaderUpgradedTo5KeepsLeadingPastItsOwnOldFrame(t *testing.T) {
	type tc struct {
		name             string
		lBefore, xBefore bool
		xTerm            uint64
		leads            bool
		term             uint64
	}
	runCase := func(t *testing.T, c tc) {
		old := confirmedLeader(0)
		old.cfg.termFromAnswers = true
		now := old.now.Add(testTiming().HeartbeatInterval)
		old.Tick(now)
		var hb core.MessageValue
		for _, m := range answersOf(old.Ready()) {
			if m.Kind == core.MsgHeartbeat && m.To == 3 {
				hb = m
			}
		}
		if hb.Term != 2 || hb.Flags&core.FlagProtocol5 != 0 {
			t.Fatalf("setup: no heartbeat of term 2 without FlagProtocol5 to X: %+v", hb)
		}
		l := restartedLeader(old, c.lBefore)
		if st := l.Status(); st.Role != core.RoleLeader || st.Term != 3 {
			t.Fatalf("setup: the restarted node is a %v at term %d, want the leader of term 3", st.Role, st.Term)
		}
		x := newTest(3, testState{term: c.xTerm}, 0, false)
		x.cfg.termFromAnswers = c.xBefore
		x.Step(l.now, hb)
		var refusal core.MessageValue
		for _, m := range answersOf(x.Ready()) {
			if m.Kind == core.MsgAppResp && m.To == 1 {
				refusal = m
			}
		}
		if !refusal.Reject {
			t.Fatalf("setup: X at term %d did not refuse the heartbeat of term 2: %+v", c.xTerm, refusal)
		}
		match, heard := l.prs[3].match, l.prs[3].lastHeard
		l.Step(l.now.Add(time.Millisecond), refusal)
		st := l.Status()
		switch {
		case (st.Role == core.RoleLeader) != c.leads || st.Term != c.term:
			t.Fatalf("the leader of term 3 read X's refusal %+v and is a %v at term %d, want leading %v at term %d", refusal, st.Role, st.Term, c.leads, c.term)
		case c.leads && l.prs[3].match != match:
			t.Fatalf("the leader of term 3 moved X's match from %d to %d on a refusal of its old frame", match, l.prs[3].match)
		case c.leads && !c.lBefore && refusal.Code == codeNotLeader && l.prs[3].lastHeard != heard:
			// A NOT_LEADER is no contact to a leader of protocol 5
			// (refusedForLaterTerm), nor a refusal of its log.
			t.Fatalf("the leader of term 3 counted X's NOT_LEADER of its old frame as contact")
		}
	}
	for _, c := range []tc{
		{name: "upgraded, X of this release at its term", xTerm: 3, leads: true, term: 3},
		{name: "upgraded, X of this release past its term", xTerm: 4, term: 4},
		{name: "upgraded, X of a previous release at its term", xBefore: true, xTerm: 3, leads: true, term: 3},
		{name: "upgraded, X of a previous release past its term", xBefore: true, xTerm: 4, term: 4},
		{name: "not upgraded, X of this release at its term", lBefore: true, xTerm: 3, leads: true, term: 3},
		{name: "not upgraded, X of this release past its term", lBefore: true, xTerm: 4, leads: true, term: 3},
		{name: "not upgraded, X of a previous release at its term", lBefore: true, xBefore: true, xTerm: 3, leads: true, term: 3},
		{name: "not upgraded, X of a previous release past its term", lBefore: true, xBefore: true, xTerm: 4, term: 4},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
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

// stuckMemberSchedule plays R1-18's case on G, F and X under seed, X stuck
// at xTerm: X is cut off while G and F elect a leader below xTerm and
// commit a write; then the network heals. It returns what it measured and
// whether seed reached the heal, or an error when the leader keeps leading
// past a heartbeat interval and a round of ticks, steps down at another
// term than X's, or X does not follow a leader with its log within 30 s.
// before names the nodes run as a release before #1472.
func stuckMemberSchedule(t *testing.T, seed, xTerm uint64, before ...core.NodeID) (stuckResult, bool, error) {
	n := newLateNet(t, seed, before...)
	n.nodes[lateX] = stuckNode(lateX, seed, xTerm)
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
			if st := n.nodes[id].Status(); st.Role == core.RoleLeader && st.Term < xTerm && st.Last >= 2 && st.Commit == st.Last {
				l = n.nodes[id]
			}
		}
		return l != nil
	}) {
		return stuckResult{}, false, nil
	}
	led := l.Status().Term
	w := []byte("w=1")
	pos, code := l.Propose(n.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(w), Data: w}})
	if code != 0 || !n.tick(time.Second, func() bool { return l.Status().Commit >= pos[0].Index }) {
		return stuckResult{}, true, fmt.Errorf("setup: the write was not committed (%v)", code)
	}
	if st := x.Status(); st.Term != xTerm || st.Last != 1 {
		return stuckResult{}, true, fmt.Errorf("setup: X at term %d, last %d", st.Term, st.Last)
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
		return res, true, fmt.Errorf("the leader of term %d kept leading for 2 s after X came back at term %d (term %d)", led, xTerm, l.Status().Term)
	case downTerm != xTerm:
		return res, true, fmt.Errorf("the leader of term %d stepped down at term %d, want X's %d", led, downTerm, xTerm)
	case res.down > bound:
		return res, true, fmt.Errorf("the leader stepped down %v after the heal, past %v", res.down, bound)
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
		return res, true, fmt.Errorf("X never followed a leader with its log within 30 s: X at term %d following %d, last %d", xs.Term, xs.Lead, xs.Last)
	}
	return res, true, nil
}

// TestAStuckMemberIsReadmittedAfterItsLeaderStepsDown is R1-18 end to end on
// three step functions: the leader G and F elected while X was cut off
// steps down at X's term within a heartbeat interval of X's return, the
// refusal of its own heartbeat carrying that term, and X follows a leader
// with the cluster's log within the longest election timeout and a
// heartbeat interval of the heal, one term ahead or ten (#1472), 16 seeds
// a case. It holds with X answering as a release before #1472 too, at its
// own term. Before, the leader stepped down at its own term and the
// cluster climbed to X's one PreVote round a term: about 1.45 s a term,
// no write committing meanwhile, 14.5 s for ten (the safety review of
// #1472, §3).
func TestAStuckMemberIsReadmittedAfterItsLeaderStepsDown(t *testing.T) {
	type tc struct {
		name   string
		xTerm  uint64
		before []core.NodeID
	}
	bound := testTiming().ElectionTimeoutMax + testTiming().HeartbeatInterval
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		var worstDown, worstBack time.Duration
		reached := 0
		for seed := uint64(1); seed <= 16; seed++ {
			res, ok, err := stuckMemberSchedule(t, seed, c.xTerm, c.before...)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			if !ok {
				continue
			}
			reached++
			worstDown, worstBack = max(worstDown, res.down), max(worstBack, res.readmitted)
		}
		switch {
		case reached == 0:
			t.Fatal("no seed reached the heal: G and F elected no leader below X's term")
		case worstBack > bound:
			t.Fatalf("X, %d terms ahead, followed a leader with its log %v after the heal at worst, past %v", c.xTerm-2, worstBack, bound)
		}
		t.Logf("%d schedules: step-down at most %v after the heal, X following a leader with its log at most %v after",
			reached, worstDown, worstBack)
	}
	for _, c := range []tc{
		{name: "X at term 3", xTerm: 3},
		{name: "X at term 5", xTerm: 5},
		{name: "X at term 12", xTerm: 12},
		{name: "X of a previous release at term 5", xTerm: 5, before: []core.NodeID{lateX}},
		{name: "X of a previous release at term 12", xTerm: 12, before: []core.NodeID{lateX}},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
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

// lateAppendResult is what D0's schedule measured over seeds 1 to 64.
type lateAppendResult struct {
	// reached counts the seeds that reached the release of the delayed
	// heartbeat, unvoted those where X's vote request was never held for F
	// (a setup defect), unrefused those whose heartbeat drew no refusal F
	// was delivered before the held vote, and violated those of the others
	// that violated the lease; first is the first violation.
	reached, unrefused, unvoted, violated int
	first                                 string
}

// lateAppendSeeds plays D0's schedule over seeds 1 to 64; before names the
// nodes run as a release before #1472. A seed whose refusal never reached F
// is counted apart: its vote met a follower the refusal never reached, so
// whatever F did with the vote judges nothing of the refusal.
func lateAppendSeeds(t *testing.T, before ...core.NodeID) lateAppendResult {
	var res lateAppendResult
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
		res.reached++
		if n.unvoted {
			res.unvoted++
			continue
		}
		if !n.refused {
			res.unrefused++
			continue
		}
		if violation != "" {
			res.violated++
			if res.first == "" {
				res.first = fmt.Sprintf("seed %d: %s", seed, violation)
			}
		}
	}
	return res
}

// TestALeaseHoldsAcrossALateAppendAnswer is D9 (b) on D0's schedule, the
// review of #1457 (#1472): F led term 2 and its heartbeat to X is delayed;
// F follows G at term 3 and has just acknowledged G's lease when X, a
// candidate of term 4, refuses that heartbeat; then X's delayed vote request
// of term 4 reaches F. Every seed that reaches the schedule keeps the lease:
// the refusal is at term 2, which F drops. So does every seed when X, or F,
// alone runs as a release before #1472: F never takes the refusal's term 4,
// or X refuses at term 2. Run F and X together as a release before #1472,
// the documented limit of #1472 (two nodes of the old version), or all of
// them, the step function before the fix, every such seed violates it, 48
// of 48. In every
// run X's refusal reaches F before the vote: a seed where it does not is a
// schedule that no longer plays the late refusal, reported apart from a
// violation.
func TestALeaseHoldsAcrossALateAppendAnswer(t *testing.T) {
	type tc struct {
		name   string
		before []core.NodeID
		caught bool
	}
	runCase := func(t *testing.T, c tc) {
		res := lateAppendSeeds(t, c.before...)
		switch {
		case res.reached == 0:
			t.Fatal("no seed reached the schedule: it no longer plays the late refusal")
		case res.unvoted > 0:
			t.Fatalf("%v: in %d of %d seeds that reached the schedule X's vote request of term 4 was never held for F", errSetup, res.unvoted, res.reached)
		case res.unrefused > 0:
			t.Fatalf("%d of %d seeds that reached the schedule delivered F no refusal of its delayed heartbeat before X's vote", res.unrefused, res.reached)
		case c.caught && res.violated == 0:
			t.Fatalf("%d seeds reached the schedule and none violated the lease: it catches nothing", res.reached)
		case !c.caught && res.violated > 0:
			t.Fatalf("%d of %d seeds that reached the schedule violated the lease; first: %s", res.violated, res.reached, res.first)
		case c.caught:
			t.Logf("%d of %d seeds violated the lease; first: %s", res.violated, res.reached, res.first)
		default:
			t.Logf("%d seeds reached the schedule, no violation", res.reached)
		}
	}
	for _, c := range []tc{
		{name: "this release"},
		{name: "X of a previous release", before: []core.NodeID{lateX}},
		{name: "F of a previous release", before: []core.NodeID{lateF}},
		{name: "F and X of a previous release", before: []core.NodeID{lateF, lateX}, caught: true},
		{name: "all of a previous release", before: []core.NodeID{lateG, lateF, lateX}, caught: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestLeaseReadsAreLinearizableUnderFaultsInAMixedCluster runs the client
// workload of TestLinearizableUnderFaults with its reads served under the
// lease, node 3 of the previous release, protocol 4, on odd seeds and nodes
// 1 and 2 on even ones (speaks), which answer older-term traffic and read a
// later term's answers as every release before #1472, and are refused an
// append or a heartbeat of an older term by this release's nodes at the
// term after their request's, which they step down at. It is a net under
// partitions, drops and crashes, not a detector of D0: the simulator delays
// a message by a few milliseconds and never plays D0's schedule, whose
// refusal is held for seconds (TestALeaseHoldsAcrossALateAppendAnswer
// plays it), and the workload passes 2 000 seeds with every node of a
// previous release (the safety review of #1472, §5.3). Eight seeds, one mix
// each: the test sorts after every seed suite but shares a Bazel shard with
// three seed parts, and both mixes passed 400 seeds swept by
// CONSENSUS_SIM_SEED.
func TestLeaseReadsAreLinearizableUnderFaultsInAMixedCluster(t *testing.T) {
	type tc struct {
		name   string
		speaks map[core.NodeID]uint32
	}
	runCase := func(t *testing.T, seed uint64, c tc) {
		opt := simOptions{seed: seed, drop: 0.03, crashRate: 0.3, lease: true, speaks: c.speaks}
		if err := linearizableRunWith(opt); err != nil {
			t.Fatalf("seed %d, %s: %v", seed, c.name, err)
		}
	}
	// An odd seed runs the first mix, an even one the second.
	mixes := []tc{
		{name: "node 3 of a previous release", speaks: map[core.NodeID]uint32{3: Protocol4}},
		{name: "nodes 1 and 2 of a previous release", speaks: map[core.NodeID]uint32{1: Protocol4, 2: Protocol4}},
	}
	eachSeed(t, seeds(t, 8), func(t *testing.T, seed uint64) {
		c := mixes[1-seed%2]
		t.Run(c.name, func(t *testing.T) { runCase(t, seed, c) })
	})
}
