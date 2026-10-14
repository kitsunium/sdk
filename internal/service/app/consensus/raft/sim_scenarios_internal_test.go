package raft

import (
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
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

// seedPart is the p-th of `parts` equal slices (p = 0..parts-1) of
// seeds(t, n): the parts together are exactly seeds(t, n), each seed once. A
// suite whose seeds are its strength is split into top-level tests rather
// than trimmed, because Bazel shards a target by TOP-LEVEL test (rules_go
// deals them out by index modulo shard_count), so one test of many seeds is
// one shard's whole CPU while the shards run side by side, and under race and
// coverage such a shard overran .bazelrc's 60 s per target: four parts of
// the 60-seed suites at eight shards still timed out on CI (#304, run
// 38549751367), so they are twelve parts each at shard_count 26. Under
// CONSENSUS_SIM_SEED the single seed belongs to the part n's slicing would
// have put it in, so it still runs exactly once.
func seedPart(t *testing.T, n, p, parts int) []uint64 {
	all := seeds(t, n)
	if len(all) == 1 && n > 1 {
		i := int((all[0] - 1) % uint64(n))
		if i < p*n/parts || i >= (p+1)*n/parts {
			return nil
		}
		return all
	}
	return all[p*n/parts : (p+1)*n/parts]
}

// eachSeed runs body once per seed, each seed its own parallel subtest: every
// seed is a simulation with no shared state, so a loop of them run one after
// another is one thread of a shard's CPU, and a serial thread is what the
// scheduler starves when the other shards' parallel seeds fill the runner
// (#304: a shard of serial seeds overran 60 s under coverage where the same
// CPU spread over threads did not).
func eachSeed(t *testing.T, seeds []uint64, body func(t *testing.T, seed uint64)) {
	for _, seed := range seeds {
		t.Run("seed="+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			body(t, seed)
		})
	}
}

func TestSimElectsAndReplicates(t *testing.T) {
	eachSeed(t, seeds(t, 20), func(t *testing.T, seed uint64) {
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
	})
}

// The 60 seeds of the safety suite, in twelve top-level tests (seedPart).
func TestSimSafetyUnderFaultsP01(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 0, 12)) }
func TestSimSafetyUnderFaultsP02(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 1, 12)) }
func TestSimSafetyUnderFaultsP03(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 2, 12)) }
func TestSimSafetyUnderFaultsP04(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 3, 12)) }
func TestSimSafetyUnderFaultsP05(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 4, 12)) }
func TestSimSafetyUnderFaultsP06(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 5, 12)) }
func TestSimSafetyUnderFaultsP07(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 6, 12)) }
func TestSimSafetyUnderFaultsP08(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 7, 12)) }
func TestSimSafetyUnderFaultsP09(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 8, 12)) }
func TestSimSafetyUnderFaultsP10(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 9, 12)) }
func TestSimSafetyUnderFaultsP11(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 10, 12)) }
func TestSimSafetyUnderFaultsP12(t *testing.T) { simSafetyUnderFaults(t, seedPart(t, 60, 11, 12)) }

func simSafetyUnderFaults(t *testing.T, seeds []uint64) {
	// Each seed is its own simulation with no shared state, so the seeds run
	// in parallel: the 60 of them are the test's strength and are all kept,
	// while the wall time fits the 60 s per-target budget .bazelrc imposes,
	// coverage instrumentation included.
	eachSeed(t, seeds, func(t *testing.T, seed uint64) {
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

// All three nodes lose power at once with writes in flight; the cluster must
// elect and commit again (D15, R2-01).
func TestSimPowerCutAllThree(t *testing.T) {
	eachSeed(t, seeds(t, 20), func(t *testing.T, seed uint64) {
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
	})
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
	eachSeed(t, seeds(t, 20), func(t *testing.T, seed uint64) {
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
	})
}

// RecentEntriesBytes bounds the payloads in memory: a peer that falls
// behind is fed from the store, and safety and progress hold.
// The 30 seeds in three top-level tests (seedPart); each part must feed a
// peer from the store at least once, which every part of the deterministic
// schedule does.
func TestSimBoundedRecentEntriesP1(t *testing.T) { simBoundedRecentEntries(t, seedPart(t, 30, 0, 3)) }

func TestSimBoundedRecentEntriesP2(t *testing.T) { simBoundedRecentEntries(t, seedPart(t, 30, 1, 3)) }

func TestSimBoundedRecentEntriesP3(t *testing.T) { simBoundedRecentEntries(t, seedPart(t, 30, 2, 3)) }

func simBoundedRecentEntries(t *testing.T, seeds []uint64) {
	var fetches atomic.Int64
	// A Cleanup, not a defer: the parallel seeds run after this function
	// returns, and a Cleanup runs once they have all finished.
	t.Cleanup(func() {
		if len(seeds) > 0 && !t.Failed() && fetches.Load() == 0 {
			t.Error("no peer was ever fed from the store")
		}
	})
	eachSeed(t, seeds, func(t *testing.T, seed uint64) {
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
		fetches.Add(int64(s.fetches))
		for _, id := range s.ids {
			n := s.nodes[id]
			if got := n.r.Status().Commit; got < want {
				t.Fatalf("seed %d: node %d stuck at %d < %d", seed, id, got, want)
			}
			if n.r.log.window > 512+64 && n.r.log.evicted < min(n.r.log.applied, n.r.log.stable) {
				t.Fatalf("seed %d: node %d holds %d payload bytes", seed, id, n.r.log.window)
			}
		}
	})
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

// The 60 seeds of the snapshot schedule, in twelve top-level tests
// (seedPart).
func TestSimSnapshotsUnderFaultsP01(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 0, 12)) }

func TestSimSnapshotsUnderFaultsP02(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 1, 12)) }

func TestSimSnapshotsUnderFaultsP03(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 2, 12)) }

func TestSimSnapshotsUnderFaultsP04(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 3, 12)) }

func TestSimSnapshotsUnderFaultsP05(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 4, 12)) }

func TestSimSnapshotsUnderFaultsP06(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 5, 12)) }

func TestSimSnapshotsUnderFaultsP07(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 6, 12)) }

func TestSimSnapshotsUnderFaultsP08(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 7, 12)) }

func TestSimSnapshotsUnderFaultsP09(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 8, 12)) }

func TestSimSnapshotsUnderFaultsP10(t *testing.T) { simSnapshotsUnderFaults(t, seedPart(t, 60, 9, 12)) }

func TestSimSnapshotsUnderFaultsP11(t *testing.T) {
	simSnapshotsUnderFaults(t, seedPart(t, 60, 10, 12))
}

func TestSimSnapshotsUnderFaultsP12(t *testing.T) {
	simSnapshotsUnderFaults(t, seedPart(t, 60, 11, 12))
}

func simSnapshotsUnderFaults(t *testing.T, seeds []uint64) {
	eachSeed(t, seeds, func(t *testing.T, seed uint64) {
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

// The negative controls of the snapshot schedule: each bug it was built to
// find must make some seed fail (review of PR #301, H1, and the older
// install the schedule found).
func TestSimSnapshotsCatchTheirBugs(t *testing.T) {
	for name, m := range map[string]mutation{
		"install below the log's base": mutInstallBelowBase,
		"snapshot ack at lastIndex":    mutSnapshotAckLastIndex,
	} {
		// One parallel subtest per bug, for the reason eachSeed gives.
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			caught := ""
			for seed := uint64(1); seed <= 200 && caught == ""; seed++ {
				if s := snapSim(seed, m); s.err != nil {
					caught = fmt.Sprintf("seed %d: %v", seed, s.err)
				}
			}
			if caught == "" {
				t.Errorf("%s: no seed of 200 caught it", name)
				return
			}
			t.Logf("%s caught, %s", name, caught)
		})
	}
}
