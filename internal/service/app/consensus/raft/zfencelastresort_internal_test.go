package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The fence's last resort (#1540, the safety review of #1482's fix C v3,
// O1): past its hard bound of marks of the current term, the least recent
// mark of the node holding the most goes and that node's new runs wait for
// the next term. Under a healthy leader, restarts spread over several
// forwarders fenced each of them in turn, the most loaded node changing as
// the counts levelled out, until an election that leader never called.
// The leader now hands its leadership over at its last resort, once a term
// at most, to a voter that holds the commit and was heard: the next term
// makes every mark an older term's, the soft bound takes them back, and no
// forwarder stays fenced.

// resortBench drives r, node 1, the leader of its term in a configuration
// of voters 1, 2 and 3, the two others acknowledging every append at once,
// while forwarders restart and forward.
type resortBench struct {
	r *Raft
	// epochs and counters are each forwarder's run and its last counter.
	epochs   map[core.NodeID]uint32
	counters map[core.NodeID]uint32
	next     uint32
	// sent is every command forward stepped, once each; appended counts
	// every entry r appended, by its body.
	sent     []core.MessageValue
	appended map[string]int
	// refusedNew counts, per forwarder, the first forward of a new run the
	// fence turned away, neither proposed nor answered.
	refusedNew map[core.NodeID]int
	// timeoutNow is the node r sent a TimeoutNow, 0 while none.
	timeoutNow core.NodeID
	// silent stops the acknowledgements of voters 2 and 3; frozen keeps
	// the target of a transfer under way from catching up, its answers
	// repeating the match it holds.
	silent, frozen bool
	// mute stops one voter's acknowledgements; lagging pins the index a
	// voter acknowledges.
	mute    map[core.NodeID]bool
	lagging map[core.NodeID]uint64
}

// newResortBench makes node 1 the leader of term 2, its fence's bounds
// soft and hard.
func newResortBench(soft, hard int) *resortBench {
	r := leaderOf(2, 0, nil, 0, false)
	r.cfg.fwdRunsSoft, r.cfg.fwdRunsHard = soft, hard
	return &resortBench{
		r: r, epochs: map[core.NodeID]uint32{}, counters: map[core.NodeID]uint32{},
		next: 1, appended: map[string]int{}, refusedNew: map[core.NodeID]int{},
		mute: map[core.NodeID]bool{}, lagging: map[core.NodeID]uint64{},
	}
}

// drain takes r's Ready until it is empty, every write durable at once,
// counts what it appended and notes a TimeoutNow; it returns the forward
// answers.
func (b *resortBench) drain() []core.MessageValue {
	var out []core.MessageValue
	for i := 0; i < 16 && b.r.HasReady(); i++ {
		rd := b.r.Ready()
		for _, e := range rd.Entries {
			if e.Kind == core.EntryCommand {
				b.appended[string(e.Data)]++
			}
		}
		for _, m := range append(rd.Messages, rd.AfterSync...) {
			switch m.Kind {
			case core.MsgForwardResp, core.MsgForwardAdminResp:
				out = append(out, m)
			case core.MsgTimeoutNow:
				b.timeoutNow = m.To
			default:
			}
		}
		if rd.NeedsWrite() {
			var idx, term uint64
			if n := len(rd.Entries); n > 0 {
				idx, term = rd.Entries[n-1].Position.Index, rd.Entries[n-1].Position.Term
			}
			b.r.StorageDone(rd.Seq, idx, term)
		}
	}
	return out
}

// ack has voters 2 and 3 acknowledge r's whole log, unless they are silent.
func (b *resortBench) ack() {
	if b.silent || b.r.role != core.RoleLeader {
		return
	}
	for _, id := range []core.NodeID{2, 3} {
		if b.mute[id] {
			continue
		}
		a := msg(core.MsgAppResp, id, 1, b.r.term)
		a.Index = b.r.log.lastIndex()
		if at, ok := b.lagging[id]; ok {
			a.Index = at
		}
		if pr := b.r.prs[id]; b.frozen && id == b.r.transferTo && pr != nil {
			a.Index = pr.match
		}
		b.r.Step(b.r.now, a)
		b.drain()
	}
}

// step moves the clock a millisecond, steps m at r, has the voters
// acknowledge and returns the answers to m.
func (b *resortBench) step(m core.MessageValue) []core.MessageValue {
	b.r.now = b.r.now.Add(time.Millisecond)
	b.r.Tick(b.r.now)
	b.drain()
	b.r.Step(b.r.now, m)
	out := b.drain()
	b.ack()
	return out
}

// restart starts a new run of from and forwards once on each lane, at
// r's term: a command, then an administrative forward.
func (b *resortBench) restart(from core.NodeID) {
	b.epochs[from], b.counters[from] = b.next, 0
	b.next++
	for i, admin := range []bool{false, true} {
		b.counters[from]++
		m := liveForward(from, b.epochs[from], b.counters[from], b.r.term)
		if admin {
			m.Kind = core.MsgForwardAdmin
		} else {
			b.sent = append(b.sent, m)
		}
		if len(b.step(m)) == 0 && i == 0 {
			b.refusedNew[from]++
		}
	}
}

// wait runs r's clock for d in steps of 100 ms, the voters acknowledging.
func (b *resortBench) wait(d time.Duration) {
	end := b.r.now.Add(d)
	for b.r.now.Before(end) {
		b.r.now = b.r.now.Add(100 * time.Millisecond)
		b.r.Tick(b.r.now)
		b.drain()
		b.ack()
	}
}

// restartAll restarts each of ids in turn, rounds times or until a
// TimeoutNow left when untilTransfer.
func (b *resortBench) restartAll(ids []core.NodeID, rounds int, untilTransfer bool) {
	for round := 0; round < rounds && (!untilTransfer || b.timeoutNow == 0); round++ {
		for _, id := range ids {
			b.restart(id)
		}
	}
}

// fenced lists the forwarders of ids whose fence stands at r's term or
// above: a new run of theirs is refused until the next term.
func (b *resortBench) fenced(ids []core.NodeID) []core.NodeID {
	var out []core.NodeID
	for _, id := range ids {
		if b.r.fwdFence.nodes[id] >= b.r.term {
			out = append(out, id)
		}
	}
	return out
}

// twice is the first command appended more than once, "" when none was.
func (b *resortBench) twice() string {
	for d, n := range b.appended {
		if n > 1 {
			return fmt.Sprintf("%q appended %d times", d, n)
		}
	}
	return ""
}

// forwarders are the nodes 2 to 2+n-1.
func forwarders(n int) []core.NodeID {
	ids := make([]core.NodeID, 0, n)
	for id := core.NodeID(2); id < core.NodeID(2+n); id++ {
		ids = append(ids, id)
	}
	return ids
}

// TestTheFenceLastResortOpensANewTerm: twelve forwarders restart in turn
// under the healthy leader of term 2, each run forwarding once on each
// lane, the fence's bounds 16 and 128. At the last resort the leader hands
// its leadership to voter 2, which holds its whole log, the fence's own
// last resort kept meanwhile; once the next term is open, every
// forwarder's new run is handled, the soft bound takes every mark of term
// 2 back, and no command was appended twice, a copy of every forward
// stepped again at the end included. Red before #1540: no
// transfer, and every forwarder fenced at term 2 in its turn.
func TestTheFenceLastResortOpensANewTerm(t *testing.T) {
	const soft, hard, n = 16, 128, 12
	b := newResortBench(soft, hard)
	ids := forwarders(n)
	b.restartAll(ids, 60, true)
	if b.timeoutNow == 0 {
		b.restartAll(ids, 60, false)
		t.Fatalf("no transfer at the last resort, the leader of term %d in office (%v): %d of %d forwarders fenced at its term %v, new runs refused per forwarder %v, %d marks",
			b.r.term, b.r.role, len(b.fenced(ids)), n, b.fenced(ids), b.refusedNew, len(b.r.fwdFence.marks))
	}
	if b.timeoutNow != 2 || b.r.Summary().Transfer != 2 {
		t.Fatalf("the last resort's transfer went to %d (Transfer %d), want voter 2, the lowest of the two holding the whole log", b.timeoutNow, b.r.Summary().Transfer)
	}
	if st := b.r.Summary(); st.ForwardLastResorts == 0 || st.ForwardResortTransfers != 1 {
		t.Fatalf("last resorts %d, transfers %d: want at least one, and one transfer", st.ForwardLastResorts, st.ForwardResortTransfers)
	}
	// The fence's own last resort stays while the transfer runs: the
	// safety never rests on it.
	if len(b.fenced(ids)) == 0 {
		t.Fatalf("at the last resort, no forwarder fenced at term %d: the fence's own last resort went", b.r.term)
	}
	// Node 2 wins term 3, and node 1 follows it; node 1 then leads term 4.
	hb := msg(core.MsgHeartbeat, 2, 1, 3)
	hb.Commit = b.r.log.committed
	b.r.Step(b.r.now, hb)
	b.drain()
	if b.r.term != 3 || b.r.role != core.RoleFollower {
		t.Fatalf("node 2's heartbeat of term 3: node 1 %v at term %d", b.r.role, b.r.term)
	}
	b.r.term, b.r.vote = 4, 1
	b.r.becomeLeader()
	b.drain()
	b.ack()
	for _, id := range ids {
		before := b.refusedNew[id]
		b.restart(id)
		if b.refusedNew[id] != before {
			t.Fatalf("term %d: forwarder %d's new run refused (fence at %d)", b.r.term, id, b.r.fwdFence.nodes[id])
		}
	}
	older := 0
	for _, k := range b.r.fwdFence.marks {
		if k.term < b.r.term {
			older++
		}
	}
	if f := b.fenced(ids); len(f) > 0 || older > 0 {
		t.Fatalf("term %d: forwarders %v fenced, %d marks of an older term kept of %d, want none fenced and every mark of term 2 taken back", b.r.term, f, older, len(b.r.fwdFence.marks))
	}
	for _, m := range b.sent {
		b.step(m)
	}
	if d := b.twice(); d != "" {
		t.Fatalf("a forward handled twice: %s", d)
	}
	t.Logf("%d forwards, %d appended, last resorts %d, transfers %d, %d marks at term %d",
		len(b.sent), len(b.appended), b.r.Summary().ForwardLastResorts, b.r.Summary().ForwardResortTransfers, len(b.r.fwdFence.marks), b.r.term)
}

// TestTheLastResortTransfersOnceATerm: a transfer the last resort started
// that fails, its target never catching up, is not started again in its
// term, however many last resorts follow, each counted; the next term's
// last resort starts the next one.
func TestTheLastResortTransfersOnceATerm(t *testing.T) {
	const soft, hard, n = 16, 128, 12
	b := newResortBench(soft, hard)
	b.frozen = true
	ids := forwarders(n)
	b.restartAll(ids, 60, false)
	for b.r.transferTo != 0 && b.r.now.Before(t0.Add(time.Minute)) {
		b.wait(time.Second)
	}
	st := b.r.Summary()
	if b.timeoutNow != 0 || st.ForwardResortTransfers != 1 || b.r.role != core.RoleLeader || b.r.transferTo != 0 || b.r.term != 2 {
		t.Fatalf("setup: TimeoutNow to %d, %d transfers, %v at term %d, transfer to %d: want one failed transfer, the leader of term 2 in office",
			b.timeoutNow, st.ForwardResortTransfers, b.r.role, b.r.term, b.r.transferTo)
	}
	// Every forwarder of ids is fenced by now: new ones restart.
	b.frozen = false
	before := st.ForwardLastResorts
	b.restartAll(forwarders(2 * n)[n:], 10, false)
	st = b.r.Summary()
	if st.ForwardLastResorts <= before || st.ForwardResortTransfers != 1 || b.r.transferTo != 0 || b.timeoutNow != 0 {
		t.Fatalf("term 2, after its failed transfer: last resorts %d then %d, %d transfers, transfer to %d, TimeoutNow to %d: want more last resorts and no second transfer",
			before, st.ForwardLastResorts, st.ForwardResortTransfers, b.r.transferTo, b.timeoutNow)
	}
	b.r.term, b.r.vote = 3, 1
	b.r.becomeLeader()
	b.drain()
	b.ack()
	b.restartAll(ids, 60, true)
	if st = b.r.Summary(); b.timeoutNow == 0 || st.ForwardResortTransfers != 2 {
		t.Fatalf("term 3: TimeoutNow to %d, %d transfers: want the last resort of the new term to transfer", b.timeoutNow, st.ForwardResortTransfers)
	}
}

// TestTheLastResortLeavesATransferUnderWay: an operator's transfer to
// voter 3 runs, its target not caught up; the last resorts that come
// meanwhile start none, and leave it as it is.
func TestTheLastResortLeavesATransferUnderWay(t *testing.T) {
	const soft, hard, n = 16, 128, 12
	b := newResortBench(soft, hard)
	b.silent = true
	if code := b.r.TransferLeadership(b.r.now, 3); code != 0 {
		t.Fatalf("setup: transfer refused %v", code)
	}
	b.restartAll(forwarders(n), 6, false)
	st := b.r.Summary()
	if st.ForwardLastResorts == 0 || st.ForwardResortTransfers != 0 || st.Transfer != 3 || b.timeoutNow != 0 {
		t.Fatalf("last resorts %d, %d transfers, transfer to %d, TimeoutNow to %d: want last resorts, none transferring, the transfer to 3 untouched",
			st.ForwardLastResorts, st.ForwardResortTransfers, st.Transfer, b.timeoutNow)
	}
}

// soloLeader makes node 1 the leader of term 2 in a configuration of one
// voter, its fence's bounds soft and hard.
func soloLeader(soft, hard int) *Raft {
	cfg := &Config{
		ID: 1, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		fwdRunsSoft: soft, fwdRunsHard: hard,
	}
	r := NewRaft(cfg, &StartState{HardState: core.HardState{Term: 1}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1)}, Now: t0})
	r.now = t0.Add(5 * time.Second)
	r.term, r.vote = 2, 1
	r.becomeLeader()
	return r
}

// TestTheLastResortWithNoVoterToTakeOver: a leader alone, a leader whose
// voters have not answered in its term, and a node that does not lead
// start no transfer at their last resort; they keep the fence's own,
// which fences the node holding the most marks until the next term, and
// count each last resort.
func TestTheLastResortWithNoVoterToTakeOver(t *testing.T) {
	const soft, hard, n = 16, 128, 12
	ids := forwarders(n)
	type tc struct {
		name string
		make func() *resortBench
	}
	//: Each case names how a last resort comes with no voter to take over.
	cases := []tc{
		{"a leader alone", func() *resortBench {
			b := newResortBench(soft, hard)
			b.r = soloLeader(soft, hard)
			return b
		}},
		{"voters that never answered in the term", func() *resortBench {
			b := newResortBench(soft, hard)
			b.silent = true
			return b
		}},
		{"a follower", func() *resortBench {
			b := newResortBench(soft, hard)
			// It started at term 1, and follows node 2 at term 2.
			b.r = newTest(1, testState{term: 1}, 0, false)
			b.r.cfg.fwdRunsSoft, b.r.cfg.fwdRunsHard = soft, hard
			b.r.now = t0
			b.r.Step(t0, msg(core.MsgHeartbeat, 2, 1, 2))
			return b
		}},
	}
	//: runCase drives one case: its restarts reach the last resort, which starts
	//: no transfer, keeps the fence's own and is counted.
	runCase := func(t *testing.T, c tc) {
		b := c.make()
		b.drain()
		role := b.r.role
		b.restartAll(ids, 6, false)
		st := b.r.Summary()
		//: A last resort that went uncounted, or a transfer started, is the defect.
		if st.ForwardLastResorts == 0 || st.ForwardResortTransfers != 0 || st.Transfer != 0 || b.timeoutNow != 0 {
			t.Fatalf("last resorts %d, %d transfers, transfer to %d, TimeoutNow to %d: want last resorts and no transfer",
				st.ForwardLastResorts, st.ForwardResortTransfers, st.Transfer, b.timeoutNow)
		}
		//: The role and the term are kept, and the fence's own last resort fenced someone.
		if b.r.role != role || b.r.term != 2 || len(b.fenced(ids)) == 0 {
			t.Fatalf("%v at term %d, forwarders fenced %v: want the role kept, term 2, and the fence's own last resort", b.r.role, b.r.term, b.fenced(ids))
		}
	}
	//: One sub-test per case, each through runCase.
	for _, x := range cases {
		t.Run(x.name, func(t *testing.T) { runCase(t, x) })
	}
}

// TestTheLastResortTimeoutNowIsTakenByAPreviousRelease: the transfer the
// last resort starts is TransferLeadership's, a TimeoutNow every release
// that runs the fence honours: a voter run as the release before #1472,
// or speaking protocol 1, campaigns on it for the next term. The bench's
// voters already answer with no protocol announcement, as the first
// release's do.
func TestTheLastResortTimeoutNowIsTakenByAPreviousRelease(t *testing.T) {
	b := newResortBench(16, 128)
	b.restartAll(forwarders(12), 60, true)
	if b.timeoutNow == 0 {
		t.Fatal("setup: no TimeoutNow at the last resort")
	}
	for _, old := range []func(*Raft){
		func(f *Raft) { f.cfg.termFromAnswers = true },
		func(f *Raft) { f.cfg.speaks = 1 },
	} {
		f := newTest(b.timeoutNow, testState{term: 2}, 0, false)
		old(f)
		now := t0.Add(5 * time.Second)
		f.Step(now, msg(core.MsgHeartbeat, 1, b.timeoutNow, 2))
		f.Ready()
		f.Step(now, msg(core.MsgTimeoutNow, 1, b.timeoutNow, 2))
		if f.term != 3 || f.role != core.RoleCandidate {
			t.Fatalf("a previous release's voter on the last resort's TimeoutNow: %v at term %d, want a candidate of term 3", f.role, f.term)
		}
	}
}

// TestTheLastResortLeavesAnOperatorsTransferUnderWay: both voters answer,
// and an operator's transfer to voter 3, which never catches up, runs when
// the last resorts come: they start none and count none, and once the
// operator's transfer failed the term still has its own, which the next
// last resort starts. Green with the guard on a transfer under way removed
// only while no voter could take over (the safety review of #1540, m4).
func TestTheLastResortLeavesAnOperatorsTransferUnderWay(t *testing.T) {
	const soft, hard, n = 16, 128, 12
	b := newResortBench(soft, hard)
	b.frozen = true
	if code := b.r.TransferLeadership(b.r.now, 3); code != 0 {
		t.Fatalf("setup: transfer refused %v", code)
	}
	b.restartAll(forwarders(n), 6, false)
	st := b.r.Summary()
	if st.ForwardLastResorts == 0 || st.ForwardResortTransfers != 0 || st.Transfer != 3 || b.timeoutNow != 0 {
		t.Fatalf("under the operator's transfer: last resorts %d, %d transfers, transfer to %d, TimeoutNow to %d: want last resorts, none transferring, the transfer to 3 untouched",
			st.ForwardLastResorts, st.ForwardResortTransfers, st.Transfer, b.timeoutNow)
	}
	for b.r.transferTo != 0 && b.r.now.Before(t0.Add(time.Minute)) {
		b.wait(time.Second)
	}
	b.frozen = false
	if b.r.role != core.RoleLeader || b.r.term != 2 || b.r.transferTo != 0 {
		t.Fatalf("setup: the operator's transfer did not fail: %v at term %d, transfer to %d", b.r.role, b.r.term, b.r.transferTo)
	}
	b.restartAll(forwarders(3 * n)[n:], 10, true)
	if st = b.r.Summary(); st.ForwardResortTransfers != 1 || b.timeoutNow == 0 || b.r.term != 2 {
		t.Fatalf("term 2, after the operator's transfer failed: %d transfers, TimeoutNow to %d, term %d: want the term's own last-resort transfer",
			st.ForwardResortTransfers, b.timeoutNow, b.r.term)
	}
}

// TestTheLastResortSkipsAVoterBehindTheCommit: voter 2 answers at once but
// holds only the leader's no-op, and voter 3, which carried the commit,
// went silent past the minimum election timeout: no voter can take over,
// and the last resort transfers to none, the laggard least of all (the
// safety review of #1540, m3).
func TestTheLastResortSkipsAVoterBehindTheCommit(t *testing.T) {
	const soft, hard, n = 16, 128, 12
	b := newResortBench(soft, hard)
	b.lagging[2] = 1
	ids := forwarders(n)
	b.restartAll(ids, 3, false)
	if len(b.r.fwdFence.marks) >= hard || b.r.log.committed <= 1 {
		t.Fatalf("setup: %d marks, commit %d", len(b.r.fwdFence.marks), b.r.log.committed)
	}
	b.mute[3] = true
	b.wait(1500 * time.Millisecond)
	if b.r.role != core.RoleLeader {
		t.Fatalf("setup: the leader stepped down with voter 2 answering")
	}
	b.restartAll(forwarders(2 * n)[n:], 6, false)
	st := b.r.Summary()
	if st.ForwardLastResorts == 0 || st.ForwardResortTransfers != 0 || st.Transfer != 0 || b.timeoutNow != 0 {
		t.Fatalf("last resorts %d, %d transfers, transfer to %d, TimeoutNow to %d: want last resorts and no transfer to voter 2, behind the commit %d",
			st.ForwardLastResorts, st.ForwardResortTransfers, st.Transfer, b.timeoutNow, b.r.log.committed)
	}
}
