package raft

import (
	"os"
	"strconv"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

func seeds(t *testing.T, n int) []uint64 {
	if v := os.Getenv("CONSENSUS_SIM_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("CONSENSUS_SIM_SEED: %v", err)
		}
		return []uint64{s}
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(i + 1)
	}
	return out
}

func TestSimElectsAndReplicates(t *testing.T) {
	for _, seed := range seeds(t, 20) {
		s := newSim(simOptions{seed: seed})
		s.run(s.after(10*time.Second), true)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		if s.leader() == nil {
			t.Fatalf("seed %d: no leader after 10 s", seed)
		}
		if c := s.maxCommit(); c < 100 {
			t.Fatalf("seed %d: commit %d after 10 s of load", seed, c)
		}
		// Every live node converges on the same applied prefix.
		s.run(s.after(3*time.Second), false)
		for _, id := range s.ids {
			if got, want := len(s.nodes[id].applied), int(s.maxCommit()); got != want {
				t.Fatalf("seed %d: node %d applied %d of %d", seed, id, got, want)
			}
		}
	}
}

func TestSimSafetyUnderFaults(t *testing.T) {
	for _, seed := range seeds(t, 60) {
		s := newSim(simOptions{seed: seed, drop: 0.05, dup: 0.02, crashRate: 0.5, diskMax: 20 * time.Millisecond})
		for k := 0; k < 6 && s.err == nil; k++ {
			switch k % 3 {
			case 0:
				s.partition([]core.NodeID{1}, []core.NodeID{2, 3})
			case 1:
				s.partition([]core.NodeID{1, 2}, []core.NodeID{3})
			default:
				s.heal()
			}
			s.run(s.after(5*time.Second), true)
		}
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		// Liveness after healing: no more faults, every node back.
		s.heal()
		s.opt.crashRate, s.opt.drop, s.opt.dup = 0, 0, 0
		for _, id := range s.ids {
			if !s.nodes[id].alive {
				s.start(s.nodes[id])
			}
		}
		before := s.maxCommit()
		s.run(s.after(10*time.Second), true)
		if s.err != nil {
			t.Fatalf("seed %d after heal: %v", seed, s.err)
		}
		if s.maxCommit() <= before {
			t.Fatalf("seed %d: no progress after healing (commit %d)", seed, before)
		}
	}
}

// All three nodes lose power at once with writes in flight; the cluster must
// elect and commit again (D15, R2-01).
func TestSimPowerCutAllThree(t *testing.T) {
	for _, seed := range seeds(t, 20) {
		s := newSim(simOptions{seed: seed, diskMax: 30 * time.Millisecond})
		s.run(s.after(5*time.Second), true)
		for _, id := range s.ids {
			s.crash(id)
		}
		s.run(s.after(500*time.Millisecond), false)
		for _, id := range s.ids {
			s.start(s.nodes[id])
		}
		before := s.maxCommit()
		s.run(s.after(10*time.Second), true)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		if s.leader() == nil || s.maxCommit() <= before {
			t.Fatalf("seed %d: no election or commit after the power cut", seed)
		}
	}
}

// CheckQuorum: a leader cut from both followers steps down within an
// election timeout, and the majority elects another.
func TestSimIsolatedLeaderStepsDown(t *testing.T) {
	s := newSim(simOptions{seed: 7})
	s.run(s.after(5*time.Second), true)
	old := s.leader()
	if old == nil {
		t.Fatal("no leader")
	}
	var rest []core.NodeID
	for _, id := range s.ids {
		if id != old.id {
			rest = append(rest, id)
		}
	}
	s.partition([]core.NodeID{old.id}, rest)
	s.run(s.after(5*time.Second), true)
	if s.err != nil {
		t.Fatal(s.err)
	}
	if old.r.Status().Role == core.RoleLeader {
		t.Fatal("isolated leader kept leading")
	}
	if l := s.leader(); l == nil || l.id == old.id {
		t.Fatal("majority did not elect")
	}
}

// R1-18: a node partitioned away campaigns without raising its term (PreVote),
// and when the partition heals it neither deposes the leader nor stays stuck.
func TestSimPreVoteKeepsLeaderAfterHeal(t *testing.T) {
	s := newSim(simOptions{seed: 11})
	s.run(s.after(5*time.Second), true)
	l := s.leader()
	if l == nil {
		t.Fatal("no leader")
	}
	term := l.r.Status().Term
	var victim core.NodeID
	for _, id := range s.ids {
		if id != l.id {
			victim = id
			break
		}
	}
	var rest []core.NodeID
	for _, id := range s.ids {
		if id != victim {
			rest = append(rest, id)
		}
	}
	s.partition([]core.NodeID{victim}, rest)
	s.run(s.after(10*time.Second), true)
	if got := s.nodes[victim].r.Status().Term; got != term {
		t.Fatalf("partitioned node raised its term to %d (was %d)", got, term)
	}
	s.heal()
	s.run(s.after(5*time.Second), true)
	if s.err != nil {
		t.Fatal(s.err)
	}
	if nl := s.leader(); nl == nil || nl.id != l.id || nl.r.Status().Term != term {
		t.Fatal("the healed node deposed a healthy leader")
	}
	if s.nodes[victim].r.Status().Commit != l.r.Status().Commit {
		s.run(s.after(2*time.Second), false)
		if s.nodes[victim].r.Status().Commit != l.r.Status().Commit {
			t.Fatal("the healed node did not converge through log repair")
		}
	}
}

func TestSimLeadershipTransfer(t *testing.T) {
	s := newSim(simOptions{seed: 3})
	s.run(s.after(5*time.Second), true)
	l := s.leader()
	if l == nil {
		t.Fatal("no leader")
	}
	var to core.NodeID
	for _, id := range s.ids {
		if id != l.id {
			to = id
		}
	}
	if code := l.r.TransferLeadership(s.now, to); code != 0 {
		t.Fatalf("transfer refused: %v", code)
	}
	s.process(l)
	s.run(s.after(time.Second), false)
	if s.err != nil {
		t.Fatal(s.err)
	}
	if nl := s.leader(); nl == nil || nl.id != to {
		t.Fatalf("transfer did not elect %d", to)
	}
}

// A transfer submitted on a FOLLOWER is forwarded to the leader, recorded as
// an entry, and completes: the follower gets the entry's position, that
// entry commits with that term, and the target leads.
func TestSimTransferForwardedFromFollower(t *testing.T) {
	for _, seed := range seeds(t, 20) {
		s := newSim(simOptions{seed: seed})
		s.run(s.after(3*time.Second), true)
		l := s.leader()
		if l == nil {
			t.Fatalf("seed %d: no leader", seed)
		}
		var f, to *simNode
		for _, id := range s.ids {
			if id != l.id {
				if f == nil {
					f = s.nodes[id]
				} else {
					to = s.nodes[id]
				}
			}
		}
		data := wire.AppendAdmin(nil, &wire.Admin{Operator: "op:test", Target: to.id})
		m := core.Message{Version: 1, Group: 1, Kind: core.MsgForwardAdmin, From: f.id, To: l.id, Term: f.r.Status().Term,
			Context: 77, Entries: []core.Entry{{Kind: core.EntryTransfer, BodySum: wire.BodySum(data), Data: data}}}
		s.deliver(m)
		s.run(s.after(3*time.Second), true)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		if nl := s.leader(); nl == nil || nl.id != to.id {
			t.Fatalf("seed %d: the forwarded transfer did not elect %d", seed, to.id)
		}
		resp, ok := s.forwarded[[2]uint64{uint64(f.id), 77}]
		if !ok || resp.Reject {
			t.Fatalf("seed %d: no position answered to the follower: %+v", seed, resp)
		}
		e, ok := f.r.Entry(resp.Index)
		if !ok || e.Position.Term != resp.LogTerm || e.Kind != core.EntryTransfer || f.r.Status().Commit < resp.Index {
			t.Fatalf("seed %d: the transfer entry is not committed at its answered position", seed)
		}
	}
}
