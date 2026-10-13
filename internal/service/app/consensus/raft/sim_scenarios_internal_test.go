package raft

import (
	"fmt"
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

// seedQuarter is the q-th of four equal slices (q = 0..3) of seeds(t, n):
// the four together are exactly seeds(t, n), each seed once. A suite whose
// seeds are its strength is split into four top-level tests rather than
// trimmed, because Bazel shards a target by TOP-LEVEL test (rules_go deals
// them out by index modulo shard_count), so one test of 60 parallel seeds is
// one shard's whole CPU while the shards run side by side, and under race
// and coverage that one shard alone overran .bazelrc's 60 s per target.
// Under CONSENSUS_SIM_SEED the single seed belongs to the quarter n's slice
// would have put it in, so it still runs exactly once.
func seedQuarter(t *testing.T, n, q int) []uint64 {
	all := seeds(t, n)
	if len(all) == 1 && n > 1 {
		if int((all[0]-1)%uint64(n))*4/n != q {
			return nil
		}
		return all
	}
	return all[q*n/4 : (q+1)*n/4]
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

// The 60 seeds of the safety suite, in four top-level tests (seedQuarter).
func TestSimSafetyUnderFaultsQ1(t *testing.T) { simSafetyUnderFaults(t, seedQuarter(t, 60, 0)) }
func TestSimSafetyUnderFaultsQ2(t *testing.T) { simSafetyUnderFaults(t, seedQuarter(t, 60, 1)) }
func TestSimSafetyUnderFaultsQ3(t *testing.T) { simSafetyUnderFaults(t, seedQuarter(t, 60, 2)) }
func TestSimSafetyUnderFaultsQ4(t *testing.T) { simSafetyUnderFaults(t, seedQuarter(t, 60, 3)) }

func simSafetyUnderFaults(t *testing.T, seeds []uint64) {
	// Each seed is its own simulation with no shared state, so the seeds run
	// in parallel: the 60 of them are the test's strength and are all kept,
	// while the wall time fits the 60 s per-target budget .bazelrc imposes,
	// coverage instrumentation included.
	for _, seed := range seeds {
		t.Run("seed="+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
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
		})
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
		data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:test", Target: to.id})
		m := core.MessageValue{
			Version: 1, Group: 1, Kind: core.MsgForwardAdmin, From: f.id, To: l.id, Term: f.r.Status().Term,
			Context: 77, Entries: []core.EntryValue{{Kind: core.EntryTransfer, BodySum: wire.BodySum(data), Data: data}},
		}
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

// RecentEntriesBytes bounds the payloads in memory: a peer that falls
// behind is fed from the store, and safety and progress hold.
func TestSimBoundedRecentEntries(t *testing.T) {
	fetches := 0
	defer func() {
		if !t.Failed() && fetches == 0 {
			t.Error("no peer was ever fed from the store")
		}
	}()
	for _, seed := range seeds(t, 30) {
		s := newSim(simOptions{seed: seed, drop: 0.02, crashRate: 0.3, recent: 512})
		for k := 0; k < 4 && s.err == nil; k++ {
			if k%2 == 0 {
				s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
			} else {
				s.heal()
			}
			s.run(s.after(4*time.Second), true)
		}
		s.heal()
		s.opt.crashRate, s.opt.drop = 0, 0
		for _, id := range s.ids {
			if !s.nodes[id].alive {
				s.start(s.nodes[id])
			}
		}
		s.run(s.after(8*time.Second), true)
		if s.err != nil {
			t.Fatalf("seed %d: %v", seed, s.err)
		}
		want := s.maxCommit()
		s.run(s.after(3*time.Second), false)
		fetches += s.fetches
		for _, id := range s.ids {
			n := s.nodes[id]
			if got := n.r.Status().Commit; got < want {
				t.Fatalf("seed %d: node %d stuck at %d < %d", seed, id, got, want)
			}
			if n.r.log.window > 512+64 && n.r.log.evicted < min(n.r.log.applied, n.r.log.stable) {
				t.Fatalf("seed %d: node %d holds %d payload bytes", seed, id, n.r.log.window)
			}
		}
	}
}

// snapSim runs one seed of the snapshot schedule: snapshots every 16
// entries, restarts that lose the snapshot's body half the time (no table,
// log kept), crashes, drops and partitions (review of PR #301, H1).
func snapSim(seed uint64, mut mutation) *sim {
	s := newSim(simOptions{
		seed: seed, drop: 0.05, dup: 0.02, crashRate: 1, diskMax: 20 * time.Millisecond,
		snapEvery: 16, snapLoss: 0.5, mut: mut,
	})
	for k := 0; k < 6 && s.err == nil; k++ {
		switch k % 3 {
		case 0:
			s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
		case 1:
			s.partition([]core.NodeID{1, 2}, []core.NodeID{3})
		default:
			s.heal()
		}
		s.run(s.after(5*time.Second), true)
	}
	return s
}

// The 60 seeds of the snapshot schedule, in four top-level tests
// (seedQuarter).
func TestSimSnapshotsUnderFaultsQ1(t *testing.T) { simSnapshotsUnderFaults(t, seedQuarter(t, 60, 0)) }

func TestSimSnapshotsUnderFaultsQ2(t *testing.T) { simSnapshotsUnderFaults(t, seedQuarter(t, 60, 1)) }

func TestSimSnapshotsUnderFaultsQ3(t *testing.T) { simSnapshotsUnderFaults(t, seedQuarter(t, 60, 2)) }

func TestSimSnapshotsUnderFaultsQ4(t *testing.T) { simSnapshotsUnderFaults(t, seedQuarter(t, 60, 3)) }

func simSnapshotsUnderFaults(t *testing.T, seeds []uint64) {
	for _, seed := range seeds {
		t.Run("seed="+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			s := snapSim(seed, 0)
			if s.err != nil {
				t.Fatalf("seed %d: %v", seed, s.err)
			}
			s.heal()
			s.opt.crashRate, s.opt.drop, s.opt.dup = 0, 0, 0
			for _, id := range s.ids {
				if !s.nodes[id].alive {
					s.start(s.nodes[id])
				}
			}
			s.run(s.after(10*time.Second), true)
			if s.err != nil {
				t.Fatalf("seed %d after heal: %v", seed, s.err)
			}
			// Every node is back to a table and caught up.
			s.run(s.after(5*time.Second), false)
			for _, id := range s.ids {
				n := s.nodes[id]
				if n.noTable || n.r.Status().Commit != s.maxCommit() {
					t.Fatalf("seed %d: node %d noTable=%v commit %d of %d", seed, id, n.noTable, n.r.Status().Commit, s.maxCommit())
				}
			}
		})
	}
}

// The negative controls of the snapshot schedule: each bug it was built to
// find must make some seed fail (review of PR #301, H1, and the older
// install the schedule found).
func TestSimSnapshotsCatchTheirBugs(t *testing.T) {
	for name, m := range map[string]mutation{
		"install below the log's base": mutInstallBelowBase,
		"snapshot ack at lastIndex":    mutSnapshotAckLastIndex,
	} {
		caught := ""
		for seed := uint64(1); seed <= 200 && caught == ""; seed++ {
			if s := snapSim(seed, m); s.err != nil {
				caught = fmt.Sprintf("seed %d: %v", seed, s.err)
			}
		}
		if caught == "" {
			t.Errorf("%s: no seed of 200 caught it", name)
			continue
		}
		t.Logf("%s caught, %s", name, caught)
	}
}
