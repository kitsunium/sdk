package raft

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The safety review of #1482's first fix (D1): a sender stopped while it
// wrote a batch — SIGSTOP, a frozen cgroup, a paused machine — resumes past
// its write deadline, dials again and writes the whole batch again, so the
// copy of a forward reaches its peer as late as the stop lasted (25.1 s
// measured with the TCP transport at its defaults, consensus's
// TestAStalledSendersLateCopyIsAppendedOnce). An answer kept for a time is
// forgotten before such a copy: at a low rate, a copy later than the window
// was proposed again, and one refused NOT_LEADER was appended once its
// receiver led. The fence keeps a forward from being handled twice whatever
// the delay of its copy, its answer kept or not, across a restart of its
// receiver too: these tests are red on the window alone.

// fenceCommand is the command node 2 forwards.
var fenceCommand = []byte("fence=withdraw-100")

// fenceForward is node 2's forward of fenceCommand at term under ctx.
func fenceForward(term, ctx uint64) core.MessageValue {
	m := msg(core.MsgForward, 2, 1, term)
	m.Context = ctx
	m.Entries = []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(fenceCommand), Data: fenceCommand}}
	return m
}

// fenceIndexes is the indexes of r's entries that carry fenceCommand.
func fenceIndexes(r *Raft) []uint64 {
	sum := wire.BodySum(fenceCommand)
	var out []uint64
	for i := range r.log.ents {
		e := &r.log.ents[i]
		if e.Kind == core.EntryCommand && e.BodySum == sum && (e.Data == nil || bytes.Equal(e.Data, fenceCommand)) {
			out = append(out, e.Position.Index)
		}
	}
	return out
}

// settleDurable hands r's Ready to a store that is durable at once.
func settleDurable(r *Raft) {
	for range 4 {
		rd := r.Ready()
		if len(rd.Entries) == 0 {
			return
		}
		last := rd.Entries[len(rd.Entries)-1].Position
		r.StorageDone(rd.Seq, last.Index, last.Term)
	}
}

// leadWithLoad keeps r, the leader of term, in office for d, a heartbeat at
// a time: it ticks, both followers acknowledge the whole log, and node 3
// forwards one command every `every`, a light load far below the 4 096
// answers the release before #1482 kept. It returns how many forwards node
// 3 sent.
func leadWithLoad(r *Raft, term uint64, d, every time.Duration) int {
	end := r.now.Add(d)
	next := r.now.Add(every)
	others := 0
	for r.now.Before(end) {
		r.now = r.now.Add(100 * time.Millisecond)
		r.Tick(r.now)
		settleDurable(r)
		for _, id := range []core.NodeID{2, 3} {
			ack := msg(core.MsgAppResp, id, 1, term)
			ack.Index = r.log.lastIndex()
			r.Step(r.now, ack)
			hb := msg(core.MsgHeartbeatResp, id, 1, term)
			hb.Index = r.log.lastIndex()
			r.Step(r.now, hb)
		}
		if every > 0 && !r.now.Before(next) {
			others++
			m := forwardCmd(term, 1<<40+uint64(others), 1)
			m.From = 3
			r.Step(r.now, m)
			next = next.Add(every)
		}
		settleDurable(r)
	}
	return others
}

// lateCopyAppendedOnce plays: node 1 leads term 2 and appends node 2's
// forward; it stays leader, answering a forward of node 3 a second; the copy
// of node 2's frame arrives `late` after the original. It returns an error
// when the command sits twice in the log.
func lateCopyAppendedOnce(late time.Duration) error {
	r := leaderOf(2, 0, nil, 0, false)
	r.Step(r.now, fenceForward(2, 9))
	first := forwardAnswers(r, 9)
	settleDurable(r)
	if len(first) != 1 || first[0].Reject {
		return fmt.Errorf("setup: the forward answered %+v", first)
	}
	others := leadWithLoad(r, 2, late, time.Second)
	if r.role != core.RoleLeader {
		return fmt.Errorf("setup: node 1 lost its leadership (role %v)", r.role)
	}
	r.Step(r.now, fenceForward(2, 9))
	again := forwardAnswers(r, 9)
	settleDurable(r)
	leadWithLoad(r, 2, time.Second, 0)
	if c := fenceIndexes(r); len(c) != 1 {
		return fmt.Errorf("copy %v late, %d other forwards answered in between: the command sits at %v (commit %d), answered %+v then %+v",
			late, others, c, r.log.committed, first[0], again)
	}
	return nil
}

// A copy later than the forward window, at a light load: on the window
// alone it was proposed again past the window, both copies committed (at 2
// and 22, 33, 63, 603 in the review); the fence answers nothing and appends
// nothing.
func TestALateCopyAtALowRateIsAppendedOnce(t *testing.T) {
	type tc struct {
		late time.Duration
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		if err := lateCopyAppendedOnce(x.late); err != nil {
			t.Error(err)
		}
	}
	for _, x := range []tc{
		{10 * time.Second}, {21 * time.Second}, {23 * time.Second}, {30 * time.Second}, {time.Minute}, {10 * time.Minute},
	} {
		runCase(t, x)
	}
}

// refusedCopyNeverAppended plays: node 1, a follower of term 2 under leader
// 3, refuses node 2's forward NOT_LEADER — D6: nothing was appended —, leads
// term 3 a second later, and the copy of node 2's frame arrives `late` after
// the original. The node started at term 1 and heard term 2 from its
// leader: it ran through the term it refused at. It returns an error when
// the refused command is appended.
func refusedCopyNeverAppended(late time.Duration) error {
	r := newTest(1, testState{term: 1}, 0, false)
	r.now = t0.Add(time.Second)
	r.Step(r.now, msg(core.MsgHeartbeat, 3, 1, 2))
	r.Ready()
	r.Step(r.now, fenceForward(2, 9))
	first := forwardAnswers(r, 9)
	if len(first) != 1 || !first[0].Reject || first[0].Code != codeNotLeader {
		return fmt.Errorf("setup: the follower answered %+v, want NOT_LEADER", first)
	}
	r.now = r.now.Add(time.Second)
	r.term, r.vote = 3, 1
	r.becomeLeader()
	settleDurable(r)
	others := leadWithLoad(r, 3, late-time.Second, time.Second)
	if r.role != core.RoleLeader {
		return fmt.Errorf("setup: node 1 lost its leadership (role %v)", r.role)
	}
	r.Step(r.now, fenceForward(2, 9))
	again := forwardAnswers(r, 9)
	settleDurable(r)
	leadWithLoad(r, 3, time.Second, 0)
	if c := fenceIndexes(r); len(c) != 0 {
		return fmt.Errorf("copy %v late, %d other forwards answered in between: refused %v (code %v), then appended at %v (commit %d), answered %+v",
			late, others, first[0].Reject, first[0].Code, c, r.log.committed, again)
	}
	return nil
}

// The forwarder was told NOT_LEADER, the one answer D6 says means "never
// appended": on the window alone, the leader that refused it proposed its
// late copy, which committed (at 20, 31, 61 in the review); the fence never
// handles it again.
func TestALateCopyOfARefusedForwardIsNeverAppended(t *testing.T) {
	type tc struct {
		late time.Duration
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		if err := refusedCopyNeverAppended(x.late); err != nil {
			t.Error(err)
		}
	}
	for _, x := range []tc{
		{10 * time.Second}, {21 * time.Second}, {23 * time.Second}, {30 * time.Second}, {time.Minute},
	} {
		runCase(t, x)
	}
}

// restartedFrom is a new run of r over what r made durable: its hard state
// and its log, as a restart loads them.
func restartedFrom(r *Raft) *Raft {
	settleDurable(r)
	cfg := r.cfg
	return NewRaft(&cfg, &StartState{
		HardState: core.HardState{Term: r.term, Vote: r.vote, Commit: r.log.committed},
		Snapshot:  core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries:   slices.Clone(r.log.ents),
		Now:       r.now.Add(time.Second),
	})
}

// A node appended a forward as the leader of term 2, then restarted — the
// table is volatile — while the forwarder was stopped mid-batch; the copy
// reaches the new run (review D1 (c)). The new run started at term 2 and
// handles no forward of a term at most that one: as a follower it does not
// refuse NOT_LEADER a command its log holds, and as the leader of term 3 it
// does not append it again. Red before the fence: refused NOT_LEADER, and
// appended twice.
func TestARestartedNodeNeverHandlesALateCopyAgain(t *testing.T) {
	type tc struct {
		name  string
		leads bool
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		r := leaderOf(2, 0, nil, 0, false)
		r.Step(r.now, fenceForward(2, 9))
		if as := forwardAnswers(r, 9); len(as) != 1 || as[0].Reject {
			t.Fatalf("setup: the forward answered %+v", as)
		}
		r = restartedFrom(r)
		r.Ready()
		if x.leads {
			r.now = r.now.Add(time.Second)
			r.term, r.vote = 3, 1
			r.becomeLeader()
			settleDurable(r)
		}
		r.Step(r.now, fenceForward(2, 9))
		as := forwardAnswers(r, 9)
		settleDurable(r)
		if c := fenceIndexes(r); len(as) != 0 || len(c) != 1 {
			t.Fatalf("%s: the copy answered %+v, the command at %v; want no answer and the command once", x.name, as, c)
		}
		if x.leads {
			// A forward of a term past the one the new run started at,
			// the term it leads, is a new one: handled.
			r.Step(r.now, fenceForward(3, 10))
			if as := forwardAnswers(r, 10); len(as) != 1 {
				t.Fatalf("%s: a forward of term 3 answered %+v, want one answer", x.name, as)
			}
			return
		}
		// The follower of term 2 meets a forward of term 3, above its own
		// term. The case is synthetic: a forward carries a term its
		// receiver led, so a term durable there, and no honest member
		// sends one past it. The term rule (stepLaterAnswer, #1472) drops
		// it before the fence: neither proposed, nor answered, nor counted
		// by the fence, the receiver's term and leader unchanged, and the
		// command of the copy still in the log once.
		lead, drops := r.lead, r.fwdDrops
		r.Step(r.now, fenceForward(3, 10))
		as = forwardAnswers(r, 10)
		settleDurable(r)
		if c := fenceIndexes(r); len(as) != 0 || len(c) != 1 || r.term != 2 || r.lead != lead || r.fwdDrops != drops {
			t.Fatalf("%s: a forward of term 3 answered %+v, the command at %v, term %d, leader %d, drops %+v; want no answer, the command once, term 2, leader %d, drops %+v",
				x.name, as, c, r.term, r.lead, r.fwdDrops, lead, drops)
		}
	}
	for _, x := range []tc{
		{"a follower of term 2", false},
		{"the leader of term 3", true},
	} {
		t.Run(x.name, func(t *testing.T) { runCase(t, x) })
	}
}

// The fence orders a run's forwards per lane: a forward of a run whose
// counter is not above the highest handled is never handled, whatever lane
// the higher one came on only for that lane, and a new run of the same
// sender, another epoch, starts its own mark. The cases run in order, on one
// leader.
func TestAForwardIsFencedOnItsOwnLane(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	type tc struct {
		name     string
		kind     core.MessageKind
		ctx      uint64
		answered bool
		appended uint64
	}
	const epoch = 7 << 32
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		m := forwardCmd(2, x.ctx, 1)
		m.Kind = x.kind
		last := r.log.lastIndex()
		r.Step(r.now, m)
		as := forwardAnswers(r, x.ctx)
		settleDurable(r)
		if (len(as) == 1) != x.answered || len(as) > 1 || r.log.lastIndex()-last != x.appended {
			t.Fatalf("%s: answered %+v, appended %d, want answered %v and %d appended", x.name, as, r.log.lastIndex()-last, x.answered, x.appended)
		}
	}
	for _, x := range []tc{
		{"a command, counter 6", core.MsgForward, epoch | 6, true, 1},
		{"a command overtaken, counter 5", core.MsgForward, epoch | 5, false, 0},
		{"an administrative forward, counter 4, on its own lane", core.MsgForwardAdmin, epoch | 4, true, 0},
		{"a command, counter 7", core.MsgForward, epoch | 7, true, 1},
		{"the copy of counter 6, its answer kept", core.MsgForward, epoch | 6, true, 0},
		{"another run of the sender, counter 1", core.MsgForward, 9<<32 | 1, true, 1},
		{"the first run's counter 5 again", core.MsgForward, epoch | 5, false, 0},
	} {
		runCase(t, x)
	}
}
