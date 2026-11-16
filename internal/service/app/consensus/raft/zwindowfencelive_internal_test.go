package raft

import (
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The safety review of #1482's fix C found the fence safe and its eviction
// a liveness defect (D1): past 4 096 runs forwarding in one term — a
// follower in a crash loop, a fleet of learners redeployed under a leader
// that stays — the run with the lowest node id went, a live forwarder that
// never restarted, and the fence's term rose to the leader's own: every
// forward of that node and every new run, the first administrative forward
// of any run included, went to OUTCOME_UNKNOWN until an election a healthy
// leader never calls. It also found the restart's justification wrong
// (O1): a refusal can leave before the term it was given at is durable;
// what holds is that a forward to a node carries a term that node led.

// liveForward is node from's forward of a command its run epoch numbers
// counter, at term.
func liveForward(from core.NodeID, epoch, counter uint32, term uint64) core.MessageValue {
	d := fmt.Appendf(nil, "live|%d|%d|%d", from, epoch, counter)
	m := msg(core.MsgForward, from, 1, term)
	m.Context = uint64(epoch)<<32 | uint64(counter)
	m.Entries = []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}}
	return m
}

// drainForwards takes r's Ready until it is empty, every write durable at
// once, and returns the forward answers it carried.
func drainForwards(r *Raft) []core.MessageValue {
	var out []core.MessageValue
	for i := 0; i < 16 && r.HasReady(); i++ {
		rd := r.Ready()
		for _, m := range append(rd.Messages, rd.AfterSync...) {
			if m.Kind == core.MsgForwardResp || m.Kind == core.MsgForwardAdminResp {
				out = append(out, m)
			}
		}
		if rd.NeedsWrite() {
			var idx, term uint64
			if n := len(rd.Entries); n > 0 {
				idx, term = rd.Entries[n-1].Position.Index, rd.Entries[n-1].Position.Term
			}
			r.StorageDone(rd.Seq, idx, term)
		}
	}
	return out
}

// stepForward steps m at r and returns the answers and how many entries it
// appended.
func stepForward(r *Raft, m core.MessageValue) ([]core.MessageValue, uint64) {
	last := r.log.lastIndex()
	r.Step(r.now, m)
	answers := drainForwards(r)
	return answers, r.log.lastIndex() - last
}

// stayLeader keeps r, the leader of its term, in office for d: it ticks,
// and both followers acknowledge; nothing is forwarded.
func stayLeader(r *Raft, d time.Duration) {
	end := r.now.Add(d)
	for r.now.Before(end) {
		r.now = r.now.Add(100 * time.Millisecond)
		r.Tick(r.now)
		drainForwards(r)
		for _, id := range []core.NodeID{2, 3} {
			ack := msg(core.MsgAppResp, id, 1, r.term)
			ack.Index = r.log.lastIndex()
			r.Step(r.now, ack)
			hb := msg(core.MsgHeartbeatResp, id, 1, r.term)
			hb.Index = r.log.lastIndex()
			r.Step(r.now, hb)
		}
		drainForwards(r)
	}
}

// TestManyRunsInATermLockNoForwarderOut: node 3 restarts 4 096 times under
// the leader of term 2, each run forwarding one command, while node 2's one
// run forwards now and then; ten minutes on, the leader still in office,
// node 2's forwards, new runs of either node and a first administrative
// forward are all handled. Red on fix C's first version (the review's
// TestRvC2Lockout): node 2 lost 10 of its 10 forwards, three new runs
// refused, until the next term.
func TestManyRunsInATermLockNoForwarderOut(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	handled := func(m core.MessageValue) bool {
		as, n := stepForward(r, m)
		return len(as) == 1 && !as[0].Reject && n == 1
	}
	const live = 0xA2
	c2 := uint32(0)
	next2 := func() core.MessageValue { c2++; return liveForward(2, live, c2, r.term) }
	if !handled(next2()) {
		t.Fatal("setup: node 2's first forward")
	}
	for e := uint32(1); e <= uint32(maxFwdRuns); e++ {
		if !handled(liveForward(3, e, 1, r.term)) {
			t.Fatalf("run %d of node 3 refused (%d marks)", e, len(r.fwdFence.marks))
		}
		if e%512 == 0 && !handled(next2()) {
			t.Fatalf("node 2 refused at node 3's run %d", e)
		}
	}
	lost := 0
	for range 10 {
		stayLeader(r, time.Minute)
		if !handled(next2()) {
			lost++
		}
	}
	refused := 0
	for _, from := range []core.NodeID{2, 3} {
		if !handled(liveForward(from, 0xC0DE0000+uint32(from), 1, r.term)) {
			refused++
		}
	}
	adm := liveForward(3, uint32(maxFwdRuns), 2, r.term)
	adm.Kind = core.MsgForwardAdmin
	if as, _ := stepForward(r, adm); len(as) != 1 {
		refused++
	}
	if r.term != 2 || r.role != core.RoleLeader || lost > 0 || refused > 0 {
		t.Fatalf("past %d runs forwarding in term 2, the leader in office (%v, term %d): node 2 lost %d of 10 forwards, %d of 3 new runs refused (%d marks)",
			maxFwdRuns+3, r.role, r.term, lost, refused, len(r.fwdFence.marks))
	}
	// The next term forgets the marks of term 2 down to soft, the least
	// recent first: node 2's live run, handled last, keeps its mark.
	r.term, r.vote = r.term+1, 1
	r.becomeLeader()
	drainForwards(r)
	if !handled(liveForward(3, 0xFFFF, 1, r.term)) || !handled(liveForward(2, live, c2+1, r.term)) {
		t.Fatalf("term %d: a new run or node 2's run refused (%d marks)", r.term, len(r.fwdFence.marks))
	}
	if len(r.fwdFence.marks) != maxFwdRuns {
		t.Fatalf("term %d: %d marks kept, want the soft ceiling %d", r.term, len(r.fwdFence.marks), maxFwdRuns)
	}
	t.Logf("term %d: %d marks", r.term, len(r.fwdFence.marks))
}

// TestAForwardToANodeCarriesATermItLed pins the hypothesis the restart rule
// rests on (the review's A3): Node.forward addresses the leader its status
// names, stamped with that status's term, and a node names a leader only
// once that leader's append or heartbeat of the node's term arrived, its
// leader forgotten at every change of term. So a forward to a node always
// carries a term that node led. The cases run in order, on one follower.
func TestAForwardToANodeCarriesATermItLed(t *testing.T) {
	r := newTest(1, testState{term: 1}, 0, false)
	r.now = t0.Add(time.Second)
	type tc struct {
		name      string
		m         core.MessageValue
		term      uint64
		lead      core.NodeID
		campaigns bool
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		if x.campaigns {
			r.campaign(false, 0, 0)
		} else {
			r.Step(r.now, x.m)
		}
		drainForwards(r)
		if st := r.Status(); st.Term != x.term || st.Lead != x.lead {
			t.Fatalf("%s: term %d, leader %d, want term %d, leader %d", x.name, st.Term, st.Lead, x.term, x.lead)
		}
	}
	for _, x := range []tc{
		{"node 3's heartbeat of term 2", msg(core.MsgHeartbeat, 3, 1, 2), 2, 3, false},
		{"node 2's answer of term 3", msg(core.MsgHeartbeatResp, 2, 1, 3), 3, 0, false},
		{"node 2's forward of term 4", liveForward(2, 1, 1, 4), 4, 0, false},
		{"node 2's heartbeat of term 4", msg(core.MsgHeartbeat, 2, 1, 4), 4, 2, false},
		{"node 3's PreVote of term 5", msg(core.MsgPreVote, 3, 1, 5), 4, 2, false},
		{"its own campaign", core.MessageValue{}, 5, 0, true},
	} {
		runCase(t, x)
	}
}

// A refusal can leave before the term it was given at is durable: the
// Ready that carries the hard state of a new term moves its own messages to
// AfterSync, and a refusal of the next turn leaves at once. Node 1 led term
// 2, hears term 3, refuses NOT_LEADER a forward of term 2, and crashes
// before term 3 is durable: restarted at term 2, it handles no copy of the
// forward, as a follower or as the leader of term 4. The forward carried a
// term node 1 led, so the fence's start covers it. Versioned from the
// review's TestRvC2Pipelined.
func TestARefusalBeforeItsTermIsDurableIsNeverFollowedByAnAppend(t *testing.T) {
	type tc struct {
		name       string
		leaderOnly bool
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		r := leaderOf(2, 0, nil, 0, false)
		r.now = r.now.Add(10 * time.Millisecond)
		r.Step(r.now, msg(core.MsgHeartbeat, 3, 1, 3))
		if rd := r.Ready(); rd.HardState == nil || rd.HardState.Term != 3 {
			t.Fatalf("setup: the hard state of term 3 is not in the Ready: %+v", rd.HardState)
		}
		f := liveForward(2, 0xB0, 1, 2)
		r.Step(r.now, f)
		rd := r.Ready()
		early := false
		for _, m := range rd.Messages {
			early = early || (m.Kind == core.MsgForwardResp && m.Context == f.Context && m.Reject)
		}
		if !early {
			t.Fatalf("setup: the refusal is not in Messages: %+v", rd.Messages)
		}
		cfg := r.cfg
		r = NewRaft(&cfg, &StartState{
			HardState: core.HardState{Term: 2, Vote: 1, Commit: r.log.committed},
			Snapshot:  core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
			Entries:   slices.Clone(r.log.ents),
			Now:       r.now.Add(time.Second),
		})
		if !x.leaderOnly {
			if as, n := stepForward(r, f); len(as) != 0 || n != 0 {
				t.Fatalf("%s: restarted at term 2, the copy answered %+v, %d appended", x.name, as, n)
			}
		}
		r.now = r.now.Add(time.Second)
		r.term, r.vote = 4, 1
		r.becomeLeader()
		drainForwards(r)
		if as, n := stepForward(r, f); len(as) != 0 || n != 0 {
			t.Fatalf("%s: the leader of term 4 answered the copy %+v and appended %d: refused NOT_LEADER, then appended", x.name, as, n)
		}
	}
	for _, x := range []tc{
		{"the copy at the restarted follower, then at the leader", false},
		{"the copy at the leader only", true},
	} {
		t.Run(x.name, func(t *testing.T) { runCase(t, x) })
	}
}

// The forwards neither proposed nor answered are counted by cause, which
// the driver publishes as consensus_forwards_dropped_total (review O4), and
// the answers forgotten at the table's bound, published as
// consensus_forward_answers_forgotten_total.
func TestForwardDropsAreCountedByCause(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	r.cfg.fwdCeiling = 1
	type tc struct {
		name string
		m    core.MessageValue
		want ForwardDropsValue
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		stepForward(r, x.m)
		if got := r.Summary().ForwardDrops; got != x.want {
			t.Fatalf("%s: %+v, want %+v", x.name, got, x.want)
		}
	}
	for _, x := range []tc{
		{"a forward handled", liveForward(2, 1, 5, 2), ForwardDropsValue{}},
		{"its run's earlier counter", liveForward(2, 1, 4, 2), ForwardDropsValue{Copies: 1}},
		{"a run of the term this node started at", liveForward(2, 2, 1, 1), ForwardDropsValue{Copies: 1, Restarted: 1}},
		{"a second forward handled", liveForward(3, 1, 1, 2), ForwardDropsValue{Copies: 1, Restarted: 1}},
		{"a new forward at the table's bound, its oldest answer forgotten", liveForward(3, 1, 2, 2), ForwardDropsValue{Copies: 1, Restarted: 1}},
		{"the copy of that answer", liveForward(3, 1, 1, 2), ForwardDropsValue{Copies: 2, Restarted: 1}},
	} {
		runCase(t, x)
	}
	r.fwdFence.nodes = map[core.NodeID]uint64{2: 2}
	runCase(t, tc{"a run of a node whose term-2 mark went", liveForward(2, 9, 1, 2), ForwardDropsValue{Copies: 2, Restarted: 1, Evicted: 1}})
	if st := r.Summary(); st.ForwardRuns != 2 || st.ForwardForgotten != 2 {
		t.Fatalf("%d runs marked, %d answers forgotten, want 2 and 2", st.ForwardRuns, st.ForwardForgotten)
	}
}
