package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #774: a silent peer the leader still held in replicate, its window full,
// was sent nothing but empty appends. It acknowledges nothing, so the window
// never moved: #769's probeBehind answered each of its heartbeat answers
// with an empty append after the window's end, which it accepted in
// silence, and the window, full, sent no entry. It never received an entry
// of the leader's term and stayed silent for as long as that leader led, one
// voter short. The transport's unreachable report, when it reached the
// leader while the peer was down, moved it to probe and hid the defect; the
// leader no longer depends on it.
//
// Every node is a step function, no progress is forced. Node 1 (A) leads
// term 2 and commits 4..30 with node 2 (B); node 3 (C) answers heartbeats
// but receives no append. A goes down; B is elected with C's vote and
// appends its no-op at 31. B probes C, backs off to 4, C accepts, and B
// moves C to replicate. A comes back, B commits 31 and fills C's window
// with old-term entries. C writes them, its acknowledgements lost, crashes,
// and is down 10 s: TruncateAndStaySilent cuts at a damaged record past its
// last entry, so no entry is dropped and C restarts SILENT(3, last+1).
func TestASilentPeerLeftInReplicateWithAFullWindowEndsItsSilence(t *testing.T) {
	type tc struct {
		name string
		// unreachable has B told, while C is down, that C is unreachable,
		// as the TCP transport does on a failed write or dial.
		unreachable bool
	}
	cfgOf := func(id core.NodeID) *Config {
		return &Config{
			ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 2, MaxInflightBytes: 1 << 20,
			MaxBatchBytes: 150, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		}
	}
	runCase := func(t *testing.T, c tc) {
		start := func(id core.NodeID) *Raft {
			return NewRaft(cfgOf(id), &StartState{
				HardState: core.HardState{Term: 1, Commit: 2}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
				Entries: chainEntries(0, 1, 1, 1), Now: t0,
			})
		}
		h := &silenceNet{t: t, nodes: map[core.NodeID]*Raft{1: start(1), 2: start(2), 3: start(3)}, down: map[core.NodeID]bool{}}
		a := h.nodes[1]
		h.now = t0.Add(3 * time.Second)
		a.Tick(h.now)
		h.settle()
		//: node 1 must lead term 2 for the scenario to start.
		if a.role != core.RoleLeader || a.term != 2 {
			t.Fatalf("setup: node 1 role %v term %d", a.role, a.term)
		}
		h.drop = func(m core.MessageValue) bool { return m.To == 3 && m.Kind == core.MsgApp }
		//: A commits 4..30 with B alone.
		for i := 4; i <= 30; i++ {
			//: a refused write ends the setup.
			if _, code := a.Propose(h.now, []core.EntryValue{silenceCmd(i)}); code != 0 {
				t.Fatalf("propose %d: %v", i, code)
			}
			h.settle()
		}
		b, cn := h.nodes[2], h.nodes[3]
		//: C must lag at 3 while A and B hold 30.
		if a.log.committed != 30 || b.log.lastIndex() != 30 || cn.log.lastIndex() != 3 {
			t.Fatalf("setup: A commit %d, B last %d, C last %d", a.log.committed, b.log.lastIndex(), cn.log.lastIndex())
		}

		// A goes down; B campaigns. C's acknowledgements to B once B moved
		// it to replicate are lost: C crashes after writing the batch,
		// before the acknowledgement, which waits for that write (D5b),
		// leaves.
		h.down[1] = true
		h.drop = func(m core.MessageValue) bool {
			return m.From == 3 && m.To == 2 && m.Kind == core.MsgAppResp && !m.Reject &&
				b.prs[3] != nil && b.prs[3].state == stateReplicate
		}
		h.now = h.now.Add(3 * time.Second)
		b.Tick(h.now)
		h.settle()
		pr := b.prs[3]
		//: B must lead and hold C in replicate.
		if b.role != core.RoleLeader || pr.state != stateReplicate {
			t.Fatalf("setup: B role %v term %d, peer C state %d", b.role, b.term, pr.state)
		}
		term := b.term

		// A comes back and follows B: B commits its no-op and fills C's
		// window; C writes it, its acknowledgements still lost.
		h.down[1] = false
		//: tick until the window is full of appends C holds.
		for range 20 {
			//: the window is full and C holds all of it.
			if len(pr.inflight) == 2 && cn.log.lastIndex() == pr.next-1 && b.log.committed == b.termStart {
				break
			}
			h.tick(100 * time.Millisecond)
		}
		//: the stall needs a full window of appends C holds.
		if pr.state != stateReplicate || len(pr.inflight) != 2 || cn.log.lastIndex() != pr.next-1 {
			t.Fatalf("setup: peer C at B %+v, C last %d: the window is not full of appends C holds", *pr, cn.log.lastIndex())
		}

		// C crashes. Its durable state: every entry it wrote, its hard
		// state.
		cLast, cCommit := cn.log.lastIndex(), cn.log.committed
		ents := make([]core.EntryValue, 0, cLast)
		//: C's log, 1 to its last index.
		for i := uint64(1); i <= cLast; i++ {
			ents = append(ents, *cn.log.at(i))
		}
		h.down[3] = true
		h.drop = nil
		//: C is down 10 s while B keeps leading with A.
		for i := range 100 {
			//: the transport's report, when the case has one.
			if c.unreachable && i == 5 {
				b.ReportUnreachable(3)
			}
			h.tick(100 * time.Millisecond)
		}
		//: B must still lead term T.
		if b.role != core.RoleLeader || b.term != term {
			t.Fatalf("setup: B lost its leadership while C was down (role %v term %d)", b.role, b.term)
		}

		// C restarts silent: SILENT(term T, the hard state's, the highest
		// its log names; index cLast+1, the truncated log's end + 1).
		ccfg := cfgOf(3)
		ccfg.SilentTerm, ccfg.SilentIndex = term, cLast+1
		cn = NewRaft(ccfg, &StartState{
			HardState: core.HardState{Term: term, Vote: 2, Commit: cCommit}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
			Entries: ents, Now: h.now,
		})
		cn.Ready()
		h.nodes[3] = cn
		h.down[3] = false
		sent := map[uint64]int{}
		empties := 0
		h.stepped = func(m core.MessageValue) {
			//: only the appends B sends C are counted.
			if m.To != 3 || m.Kind != core.MsgApp {
				return
			}
			//: an empty append feeds C nothing.
			if len(m.Entries) == 0 {
				empties++
			}
			//: each entry B sends C.
			for i := range m.Entries {
				sent[m.Entries[i].Position.Index]++
			}
		}
		//: 10 s under a leader that keeps leading.
		for range 100 {
			h.tick(100 * time.Millisecond)
		}
		//: the scenario tests a leader that keeps leading.
		if b.role != core.RoleLeader || b.term != term {
			t.Fatalf("B lost its leadership (role %v term %d)", b.role, b.term)
		}
		//: C must have received B's term and ended its silence.
		if cn.cfg.SilentIndex != 0 || cn.log.lastIndex() < b.termStart {
			t.Fatalf("after 10 s under a leader that kept leading, C holds through %d (B's term starts at %d), silent %v; peer C at B %+v; B sent it %d empty appends and the entries %v",
				cn.log.lastIndex(), b.termStart, cn.cfg.SilentIndex != 0, *pr, empties, sent)
		}
		//: no entry C held, nor any other, is sent twice (#767).
		for idx, n := range sent {
			//: C held through cLast when it restarted.
			if idx <= cLast || n > 1 {
				t.Fatalf("entry %d sent %d times to C, which restarted holding through %d (sent %v)", idx, n, cLast, sent)
			}
		}
	}
	//: with and without the transport's unreachable report.
	for _, c := range []tc{
		{name: "no unreachable report reached the leader"},
		{name: "the transport reported the peer unreachable", unreachable: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
