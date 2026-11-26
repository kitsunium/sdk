package raft

import (
	"errors"
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A node of protocol 5 answers an append or a heartbeat of an older term
// from a leader that announces 5 (FlagProtocol5) at that term, and from a
// leader of a previous release at the term after that one, and its
// refusal, NOT_LEADER, is no report of a truncation (#1472). A leader of a
// previous release read a refusal of its own term as one of its log: it
// counted it as contact, so CheckQuorum never deposed it once its other
// followers failed, and from a silent member it read FlagTruncated,
// lowered the member's match to 0 and resent its log from index 1 at every
// refusal (the safety review of #1472, §1 and §4.1). Refused at the next
// term, it steps down at once, as it does at any later term; sent nothing,
// as the second version did, it kept leading while another member
// acknowledged it (O1 of the safety review of that version). The mutation
// type is full: a node run as a release before #1472 (termFromAnswers) is
// the previous release, and a refusal flagged in flight the mutation of
// the flag.

// silentStuckX is X (node 3) left silent by TruncateAndStaySilent at
// (SilentTerm 2, SilentIndex 4) over entries 1..3 of term 1, which since
// took term 3 from a candidate's vote request, which a silent node takes
// without granting it; before runs it as a release before #1472.
func silentStuckX(before bool) *Raft {
	r := NewRaft(&Config{
		ID: 3, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		SilentTerm: 2, SilentIndex: 4,
	}, &StartState{
		HardState: core.HardState{Term: 3, Commit: 3}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: chainEntries(0, 1, 1, 1, 1), Now: t0,
	})
	r.cfg.termFromAnswers = before
	r.Ready()
	return r
}

// silentExchange is what a leader and a silent X sent each other.
type silentExchange struct {
	// exchanges counts the rounds in which the leader sent X anything.
	exchanges int
	// appends and entries count the appends the leader sent X past its
	// first heartbeat, and the entries they carried.
	appends, entries int
	// truncated counts X's refusals carrying FlagTruncated as they left X.
	truncated int
	// leads and term are the leader's role and term at the end.
	leads bool
	term  uint64
}

// silentRun names who runs as a release before #1472 in exchangeWithSilent,
// and the mutation in flight.
type silentRun struct {
	// lBefore runs the leader so from the start, its heartbeat included;
	// lReadsBefore once its heartbeat left, a leader of a previous release
	// reading a refusal this release sends a leader of protocol 5.
	lBefore, lReadsBefore bool
	// xBefore runs X so.
	xBefore bool
	// flag adds FlagTruncated in flight to every refusal X sends.
	flag bool
}

// exchangeWithSilent has node 1 lead term 2 over entries 1..3 of term 1 and
// its no-op, X having acknowledged it through 3, heartbeat X and exchange
// every message between the two at one instant, 100 rounds at most, as run
// says.
func exchangeWithSilent(run silentRun) (silentExchange, error) {
	var res silentExchange
	l := leaderOf(2, 3, chainEntries(0, 1, 1, 1, 1), 0, false)
	l.cfg.termFromAnswers = run.lBefore
	ack := msg(core.MsgAppResp, 3, 1, 2)
	ack.Index = 3
	l.Step(l.now, ack)
	lateDrain(l)
	if pr := l.prs[3]; pr == nil || pr.match != 3 {
		return res, errors.New("setup: X's match at the leader is not 3")
	}
	x := silentStuckX(run.xBefore)
	now := l.now.Add(testTiming().HeartbeatInterval)
	l.Tick(now)
	toX := addressedTo(lateDrain(l), 3)
	l.cfg.termFromAnswers = run.lBefore || run.lReadsBefore
	for i := 0; i < 100 && len(toX) > 0; i++ {
		res.exchanges++
		for _, m := range toX {
			if m.Kind == core.MsgApp {
				res.appends++
				res.entries += len(m.Entries)
			}
			x.Step(now, m)
		}
		for _, m := range addressedTo(lateDrain(x), 1) {
			if m.Kind == core.MsgAppResp && m.Flags&core.FlagTruncated != 0 {
				res.truncated++
			}
			if run.flag && m.Kind == core.MsgAppResp && m.Reject {
				m.Flags |= core.FlagTruncated
			}
			l.Step(now, m)
		}
		toX = addressedTo(lateDrain(l), 3)
	}
	st := l.Status()
	res.leads, res.term = st.Role == core.RoleLeader, st.Term
	return res, nil
}

// addressedTo keeps the messages of ms addressed to id.
func addressedTo(ms []core.MessageValue, id core.NodeID) []core.MessageValue {
	var out []core.MessageValue
	for _, m := range ms {
		if m.To == id {
			out = append(out, m)
		}
	}
	return out
}

// TestASilentMembersRefusalOfAnOlderTermIsNoTruncationReport: X, silent and
// stuck at term 3, is heartbeat by a leader of term 2 that X acknowledged
// through index 3. No leader, of this release or a previous one, resends
// its log on what X answers, and no refusal X sends carries FlagTruncated:
// read by a leader of a previous release, the refusal X sends a leader of
// protocol 5 is ignored. On the release before this rule, a leader of a
// previous release resent X its whole log from index 1 at every refusal, a
// hundred times in a row; the refusal flagged in flight shows the check
// catches the flag.
func TestASilentMembersRefusalOfAnOlderTermIsNoTruncationReport(t *testing.T) {
	type tc struct {
		name   string
		run    silentRun
		resent bool
	}
	runCase := func(t *testing.T, c tc) {
		res, err := exchangeWithSilent(c.run)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d exchanges, %d appends carrying %d entries, %d refusals flagged truncated; the leader leads %v at term %d",
			res.exchanges, res.appends, res.entries, res.truncated, res.leads, res.term)
		switch {
		case c.resent && res.appends == 0:
			t.Fatal("a refusal flagged a truncation in flight made no leader of a previous release resend its log: the check catches nothing")
		case c.resent:
		case res.appends != 0:
			t.Fatalf("the leader resent X %d appends carrying %d entries in %d exchanges on X's refusal of its heartbeat of term 2",
				res.appends, res.entries, res.exchanges)
		case !c.run.xBefore && res.truncated != 0:
			t.Fatalf("X sent %d refusals of a heartbeat of term 2 flagged as a truncation report", res.truncated)
		}
	}
	for _, c := range []tc{
		{name: "both of this release"},
		{name: "a leader of a previous release", run: silentRun{lBefore: true}},
		{name: "this release's refusal read by a previous release", run: silentRun{lReadsBefore: true}},
		{name: "X of a previous release", run: silentRun{xBefore: true}},
		{name: "both of a previous release", run: silentRun{lBefore: true, xBefore: true}},
		{name: "this release's refusal flagged in flight, read by a previous release", run: silentRun{lReadsBefore: true, flag: true}, resent: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// stuckBehindHeal has G and F elect a leader below xTerm and commit while
// X, stuck at xTerm, is cut off, then heals X's links. It returns the net,
// the leader and whether seed reached the heal; before names the nodes run
// as a release before #1472.
func stuckBehindHeal(t *testing.T, seed, xTerm uint64, before ...core.NodeID) (*lateNet, core.NodeID, bool) {
	n := newLateNet(t, seed, before...)
	n.nodes[lateX] = stuckNode(lateX, seed, xTerm)
	for _, id := range before {
		n.nodes[id].cfg.termFromAnswers = true
	}
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.From == lateX || m.To == lateX {
			return lateDrop
		}
		return lateDeliver
	}
	var lid core.NodeID
	ok := n.tick(10*time.Second, func() bool {
		for _, id := range []core.NodeID{lateG, lateF} {
			if st := n.nodes[id].Status(); st.Role == core.RoleLeader && st.Term < xTerm && st.Last >= 2 && st.Commit == st.Last {
				lid = id
			}
		}
		return lid != 0
	})
	n.policy = func(core.MessageValue) lateVerdict { return lateDeliver }
	return n, lid, ok
}

// firstWrite hands a write to any leader among the nodes live names, once
// per leadership, and returns how long after now one of them committed, 0
// when none did within limit.
func firstWrite(n *lateNet, live []core.NodeID, limit time.Duration) time.Duration {
	start := n.now
	var by *Raft
	var byTerm, at uint64
	var back time.Duration
	n.tick(limit, func() bool {
		for _, id := range live {
			r := n.nodes[id]
			st := r.Status()
			if st.Role != core.RoleLeader {
				continue
			}
			if by != r || byTerm != st.Term {
				w := []byte("w=2")
				pos, code := r.Propose(n.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(w), Data: w}})
				if code == 0 {
					by, byTerm, at = r, st.Term, pos[0].Index
				}
				continue
			}
			if st.Commit >= at {
				back = n.now.Sub(start)
				return true
			}
		}
		return false
	})
	return back
}

// outageSchedule has G and F elect a leader and commit while X, stuck at
// xTerm, is cut off; at the heal one node of G and F fails for good, the
// leader when failLeader, its other follower otherwise, leaving two live
// voters of three, a quorum. Each step, a leader among the live nodes is
// handed a write, once per leadership. It returns how long after the heal
// one of those writes committed, 0 when none did within limit, the state
// at the end, and whether seed reached the heal. before names the nodes
// run as a release before #1472.
func outageSchedule(t *testing.T, seed, xTerm uint64, failLeader bool, limit time.Duration, before ...core.NodeID) (time.Duration, string, bool) {
	n, lid, ok := stuckBehindHeal(t, seed, xTerm, before...)
	if !ok {
		return 0, "", false
	}
	other := lateG
	if lid == lateG {
		other = lateF
	}
	failed, kept := other, lid
	if failLeader {
		failed, kept = lid, other
	}
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.From == failed || m.To == failed {
			return lateDrop
		}
		return lateDeliver
	}
	back := firstWrite(n, []core.NodeID{kept, lateX}, limit)
	ks, xs := n.nodes[kept].Status(), n.nodes[lateX].Status()
	return back, fmt.Sprintf("node %d %v at term %d, commit %d of %d; X %v at term %d following %d",
		kept, ks.Role, ks.Term, ks.Commit, ks.Last, xs.Role, xs.Term, xs.Lead), true
}

// gapBound is how long after the heal a write must commit, or X follow a
// leader with its log, X gap terms ahead of the leader of G and F: a
// heartbeat interval and one maximum election timeout a term of gap when a
// node of a previous release must climb to X's term, X of this release,
// and at most two otherwise. A node of a previous release reads Term
// alone, so a refusal moves it to the term it asked for and no further:
// any later term raised a reader that had since followed a live leader
// past D5's live-leader refusal (D0), and the vote request that would
// carry X's term made X a candidate without a won PreVote (O2 of the safety
// review of #1472's second version, relaxed under the owner's rule of
// 2026-11-14: ADR 0152's amendment of 2026-11-16).
func gapBound(gap uint64, climbs bool) time.Duration {
	tm := testTiming()
	rounds := min(gap, 2)
	if climbs {
		rounds = gap
	}
	return tm.HeartbeatInterval + time.Duration(rounds)*tm.ElectionTimeoutMax
}

// gapCase is one mix of releases and one gap of the mixed-cluster
// schedules: climbs says whether a node of a previous release must climb
// to the term of X, of this release, gap terms ahead.
type gapCase struct {
	name   string
	before []core.NodeID
	climbs bool
	gap    uint64
}

// mixedGaps are the cases the mixed-cluster schedules run: every mix, X
// one, three, six and ten terms ahead.
func mixedGaps() []gapCase {
	var out []gapCase
	for _, m := range []gapCase{
		{name: "this release"},
		{name: "G and F of a previous release", before: []core.NodeID{lateG, lateF}, climbs: true},
		{name: "G of a previous release", before: []core.NodeID{lateG}, climbs: true},
		{name: "F of a previous release", before: []core.NodeID{lateF}, climbs: true},
		{name: "X of a previous release", before: []core.NodeID{lateX}},
		{name: "all of a previous release", before: []core.NodeID{lateG, lateF, lateX}},
	} {
		for _, gap := range []uint64{1, 3, 6, 10} {
			c := m
			c.name, c.gap = fmt.Sprintf("%s, X %d terms ahead", m.name, gap), gap
			out = append(out, c)
		}
	}
	return out
}

// TestAMixedClusterWritesAgainAfterAFailure: X comes back stuck one, three,
// six or ten terms ahead just as one node of G and F fails for good, the
// leader's other follower or the leader, two live voters of three. A write
// commits within gapBound of the heal, 16 seeds a case. G and F of a
// previous release beside X of this one is the review's case (§4.1): X
// answered the old leader's heartbeats with a refusal it counted as
// contact, so CheckQuorum never deposed it and no write committed in 30 s,
// 16 schedules of 16. Then X sent it nothing and CheckQuorum deposed it,
// and the cluster climbed to X's term one PreVote round a term: 2.88, 6,
// 10.56 and 17.44 s at gaps 1, 3, 6 and 10, against 1.9 s in a cluster of
// this release (O2 of the safety review of #1472's second version). X now
// refuses it at the next term, which it steps down at, and a write commits
// within 1.9 s one term ahead, as in a cluster of this release.
func TestAMixedClusterWritesAgainAfterAFailure(t *testing.T) {
	type tc struct {
		gapCase
		failLeader bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		bound := gapBound(c.gap, c.climbs)
		var worst time.Duration
		reached := 0
		for seed := uint64(1); seed <= 16; seed++ {
			back, state, ok := outageSchedule(t, seed, 2+c.gap, c.failLeader, 30*time.Second, c.before...)
			if !ok {
				continue
			}
			reached++
			if back == 0 || back > bound {
				t.Fatalf("seed %d: a write committed %v after the heal (0: none within 30 s), past %v, two live voters of three: %s", seed, back, bound, state)
			}
			worst = max(worst, back)
		}
		if reached == 0 {
			t.Fatal("no seed reached the heal: G and F elected no leader below X's term")
		}
		t.Logf("%d schedules, a write committed at most %v after the heal (bound %v)", reached, worst, bound)
	}
	for _, g := range mixedGaps() {
		for _, failLeader := range []bool{false, true} {
			c := tc{gapCase: g, failLeader: failLeader}
			c.name += ", the leader's follower fails"
			if failLeader {
				c.name = g.name + ", the leader fails"
			}
			t.Run(c.name, func(t *testing.T) { runCase(t, c) })
		}
	}
}

// readmittedUnderQuorum heals X's links after stuckBehindHeal, nobody
// failing, and returns how long after the heal X followed a leader with
// its log, 0 when it did not within limit, and the state at the end.
func readmittedUnderQuorum(t *testing.T, seed, xTerm uint64, limit time.Duration, before ...core.NodeID) (time.Duration, string, bool) {
	n, lid, ok := stuckBehindHeal(t, seed, xTerm, before...)
	if !ok {
		return 0, "", false
	}
	x := n.nodes[lateX]
	start := n.now
	var back time.Duration
	n.tick(limit, func() bool {
		if followsWithLog(n, x) {
			back = n.now.Sub(start)
			return true
		}
		return false
	})
	ls, xs := n.nodes[lid].Status(), x.Status()
	return back, fmt.Sprintf("node %d, leader at the heal, %v at term %d; X %v at term %d following %d, last %d",
		lid, ls.Role, ls.Term, xs.Role, xs.Term, xs.Lead, xs.Last), true
}

// followsWithLog reports whether x leads with its log committed or follows a
// leader of its term whose log and commit it holds.
func followsWithLog(n *lateNet, x *Raft) bool {
	xs := x.Status()
	if xs.Role == core.RoleLeader {
		return xs.Commit == xs.Last
	}
	if xs.Lead == 0 || xs.Role != core.RoleFollower {
		return false
	}
	ls := n.nodes[xs.Lead].Status()
	return ls.Role == core.RoleLeader && ls.Term == xs.Term && xs.Last == ls.Last && xs.Commit == ls.Commit
}

// TestAMemberAheadIsReadmittedUnderALeaderThatKeepsItsQuorum: X comes back
// stuck one, three, six or ten terms ahead of the leader of G and F, and
// nobody fails. X follows a leader with the cluster's log within gapBound
// of the heal, 16 seeds a case. A leader of a previous release, sent
// nothing by X, kept leading on F's acknowledgements and X stayed out for
// as long, 16 seeds of 16 with G and F of a previous release, 10 of 16 with
// G alone, still out after 120 s (O1 of the safety review of #1472's second
// version): refused at the next term, it steps down there.
func TestAMemberAheadIsReadmittedUnderALeaderThatKeepsItsQuorum(t *testing.T) {
	type tc struct {
		gapCase
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		bound := gapBound(c.gap, c.climbs)
		var worst time.Duration
		reached := 0
		for seed := uint64(1); seed <= 16; seed++ {
			back, state, ok := readmittedUnderQuorum(t, seed, 2+c.gap, 30*time.Second, c.before...)
			if !ok {
				continue
			}
			reached++
			if back == 0 || back > bound {
				t.Fatalf("seed %d: X followed a leader with its log %v after the heal (0: not within 30 s), past %v: %s", seed, back, bound, state)
			}
			worst = max(worst, back)
		}
		if reached == 0 {
			t.Fatal("no seed reached the heal: G and F elected no leader below X's term")
		}
		t.Logf("%d schedules, X readmitted at most %v after the heal (bound %v)", reached, worst, bound)
	}
	for _, g := range mixedGaps() {
		c := tc{gapCase: g}
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// failedTransfer has G and F elect a leader whose log X holds, all three
// in touch, then the leader transfers its leadership to X (a planned stop,
// R8-17) and X's vote requests are lost: X is a candidate one term ahead of
// the leader, which takes its leadership back at the transfer's deadline.
// It returns how long after X's campaign X followed a leader with its log,
// 0 when it did not within limit, the state at the end, and whether seed
// reached the campaign. before names the nodes run as a release before
// #1472.
func failedTransfer(t *testing.T, seed uint64, limit time.Duration, before ...core.NodeID) (time.Duration, string, bool) {
	n := newLateNet(t, seed, before...)
	n.policy = func(core.MessageValue) lateVerdict { return lateDeliver }
	x := n.nodes[lateX]
	var l *Raft
	if !n.tick(10*time.Second, func() bool {
		for _, id := range []core.NodeID{lateG, lateF} {
			r := n.nodes[id]
			if st := r.Status(); st.Role == core.RoleLeader && st.Commit == st.Last && x.Status().Lead == id && x.Status().Last == st.Last {
				l = r
			}
		}
		return l != nil
	}) {
		return 0, "", false
	}
	n.tick(200*time.Millisecond, nil)
	led := l.Status().Term
	if l.Status().Role != core.RoleLeader || l.TransferLeadership(n.now, lateX) != 0 {
		return 0, "", false
	}
	n.policy = func(m core.MessageValue) lateVerdict {
		if m.Kind == core.MsgVote && m.From == lateX {
			return lateDrop
		}
		return lateDeliver
	}
	if !n.tick(time.Second, func() bool { st := x.Status(); return st.Role == core.RoleCandidate && st.Term == led+1 }) {
		return 0, "", false
	}
	n.policy = func(core.MessageValue) lateVerdict { return lateDeliver }
	start := n.now
	var back time.Duration
	n.tick(limit, func() bool {
		if followsWithLog(n, x) {
			back = n.now.Sub(start)
			return true
		}
		return false
	})
	ls, xs := l.Status(), x.Status()
	return back, fmt.Sprintf("leader of term %d now %v at term %d; X %v at term %d following %d", led, ls.Role, ls.Term, xs.Role, xs.Term, xs.Lead), true
}

// TestAFailedTransferLeavesNoMemberOut: the leader of G and F transfers its
// leadership to X, which holds its log, and X's vote requests are lost (O1
// of the safety review of #1472's second version, §3.1 (c)). X follows a
// leader with its log, or leads, within a heartbeat interval and the
// longest election timeout of its failed campaign, whatever the releases,
// 16 seeds a case. A leader of a previous release beside X of this one,
// sent nothing by X one term ahead, took its leadership back and kept it on
// F's acknowledgements: X was still out after 120 s, 10 seeds of 10, and at
// three voters the rolling upgrade's gate, a failure tolerance of at least
// one, never opened again.
func TestAFailedTransferLeavesNoMemberOut(t *testing.T) {
	type tc struct {
		name   string
		before []core.NodeID
	}
	bound := testTiming().HeartbeatInterval + testTiming().ElectionTimeoutMax
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		var worst time.Duration
		reached := 0
		for seed := uint64(1); seed <= 16; seed++ {
			back, state, ok := failedTransfer(t, seed, 30*time.Second, c.before...)
			if !ok {
				continue
			}
			reached++
			if back == 0 || back > bound {
				t.Fatalf("seed %d: X followed a leader with its log %v after its failed campaign (0: not within 30 s), past %v: %s", seed, back, bound, state)
			}
			worst = max(worst, back)
		}
		if reached == 0 {
			t.Fatal("no seed reached X's campaign: the transfer was never played")
		}
		t.Logf("%d schedules, X back at most %v after its failed campaign (bound %v)", reached, worst, bound)
	}
	for _, c := range []tc{
		{name: "this release"},
		{name: "G and F of a previous release", before: []core.NodeID{lateG, lateF}},
		{name: "G of a previous release", before: []core.NodeID{lateG}},
		{name: "F of a previous release", before: []core.NodeID{lateF}},
		{name: "all of a previous release", before: []core.NodeID{lateG, lateF, lateX}},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
