package consensus

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// founderTiming is a valid InitialTiming whose election window grows with
// k: each one passes Bootstrap's and NewNode's checks on its own, and
// Bootstrap, which contacts no peer, cannot compare them (#698).
func founderTiming(k int) coreconsensus.TimingContractSpec {
	lo := time.Duration(100+50*k) * time.Millisecond
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 10 * time.Millisecond,
		ElectionTimeoutMin: lo, ElectionTimeoutMax: 2 * lo, MaxClockDrift: 0.05,
	}
}

// foundersWith bootstraps and starts three founders, the identical member
// list on each, founder k with timing(k).
func foundersWith(t *testing.T, cid byte, timing func(k int) coreconsensus.TimingContractSpec) *cluster {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x69, 0x8d, cid}, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.InitialTiming = timing(slices.Index(c.nodes, tn))
	}
	return newClusterWith(t, c)
}

// commitEverywhere waits until every founder holds commit n, and fails the
// test naming the ones that do not.
func commitEverywhere(t *testing.T, c *cluster, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, tn := range c.nodes {
		for tn.node.Status().Commit < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if st := tn.node.Status(); st.Commit < n {
			t.Errorf("founder %d holds commit %d, not %d", tn.id, st.Commit, n)
		}
		if r, ok := tn.node.StopReason(); ok {
			t.Errorf("founder %d stopped: %v", tn.id, r.Err)
		}
	}
}

// oneEntry1 fails the test unless every founder holds the same entry 1,
// committed, and runs the same generation 1 contract.
func oneEntry1(t *testing.T, c *cluster) {
	t.Helper()
	first := must(c.nodes[0].log.Entries(1, 2, 1<<20, nil))(t)[0]
	timing := c.nodes[0].node.Status().Timing
	for _, tn := range c.nodes[1:] {
		e := must(tn.log.Entries(1, 2, 1<<20, nil))(t)[0]
		if e.Position != first.Position || e.BodySum != first.BodySum || e.ChainSum != first.ChainSum {
			t.Errorf("founder %d holds entry 1 %08x/%08x, founder %d %08x/%08x", tn.id, e.BodySum, e.ChainSum, c.nodes[0].id, first.BodySum, first.ChainSum)
		}
		if st := tn.node.Status().Timing; st != timing || st.Generation != 1 {
			t.Errorf("founder %d runs generation %d with ElectionTimeoutMin %v, founder %d generation %d with %v",
				tn.id, st.Generation, st.ElectionTimeoutMin, c.nodes[0].id, timing.Generation, timing.ElectionTimeoutMin)
		}
	}
}

// TestBootstrapLeavesEntry1Uncommitted is #698's cause: Bootstrap contacts
// no peer, and entry 1 carries the founder's own InitialTiming, so two
// founders given different timings write two entries at (1, 1). Written
// committed, each founder committed its own, and Log Matching was broken
// by construction. Entry 1 is now committed as any entry is, once a leader
// holds it on a majority.
func TestBootstrapLeavesEntry1Uncommitted(t *testing.T) {
	log, cid := NewMemoryLog(), coreconsensus.ClusterID{0x69, 0x8d, 0x10}
	id, inc, err := InitLog(log, cid, "slot-0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Cluster: cid, Log: log, InitialTiming: founderTiming(0), MinVoters: 1}
	ms := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "slot-0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), cfg, ms); err != nil {
		t.Fatal(err)
	}
	if hs := must(log.HardState())(t); hs.Term != 1 || hs.Commit != 0 {
		t.Fatalf("Bootstrap wrote %+v: entry 1 committed by no majority", hs)
	}
	if first, last := log.Bounds(); first != 1 || last != 1 {
		t.Fatalf("log [%d, %d], not entry 1 alone", first, last)
	}
}

// TestAFounderWithAnotherTimingTakesTheCommittedEntry1 is #698 with one odd
// founder: its entry 1, written from its own InitialTiming, differs from
// the one the two others share. It refused every append of their leader,
// silently, and the cluster ran with no failure tolerance. It now takes
// the leader's entry 1, uncommitted until then, replicates every write and
// runs the contract that entry carries; nobody stops.
func TestAFounderWithAnotherTimingTakesTheCommittedEntry1(t *testing.T) {
	c := foundersWith(t, 1, func(k int) coreconsensus.TimingContractSpec { return founderTiming(min(k, 1)) })
	for i := range 20 {
		proposeOnLeader(t, c, "k=v"+string(rune('a'+i)))
	}
	commitEverywhere(t, c, 21)
	oneEntry1(t, c)
}

// TestFoundersWithThreeTimingsCommitOneEntry1: three founders, three
// entries 1. No leader's append was ever accepted, and nothing committed
// for good. The first leader's entry 1 is now the cluster's: every write
// commits on all three, each holding that one entry 1 and its contract.
func TestFoundersWithThreeTimingsCommitOneEntry1(t *testing.T) {
	c := foundersWith(t, 2, founderTiming)
	for i := range 5 {
		proposeOnLeader(t, c, "k=v"+string(rune('a'+i)))
	}
	commitEverywhere(t, c, 6)
	oneEntry1(t, c)
}

// TestFoundersWithOneTimingReplicateEverywhere is the control: the same
// three founders with one timing hold one entry 1, and no node stops.
func TestFoundersWithOneTimingReplicateEverywhere(t *testing.T) {
	c := foundersWith(t, 3, func(int) coreconsensus.TimingContractSpec { return founderTiming(1) })
	for i := range 20 {
		proposeOnLeader(t, c, "k=v"+string(rune('a'+i)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, tn := range c.nodes {
		for tn.node.Status().Commit < 21 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if st := tn.node.Status(); st.Commit < 21 {
			t.Errorf("founder %d holds commit %d with identical timings", tn.id, st.Commit)
		}
		if r, ok := tn.node.StopReason(); ok {
			t.Errorf("founder %d stopped: %v", tn.id, r.Err)
		}
	}
}

// TestAFounderOfAnotherMemberListStopsOnceTheOthersCommit: Bootstrap
// compares no list between founders, and a founder given another one
// holds another entry 1 it may never take the leader's for. Left
// uncommitted, it refused every leader's chain for good, with nothing
// said; it now stops BOOTSTRAP_REFUSED once the two others commit past
// entry 1, and they keep writing (#698's review).
func TestAFounderOfAnotherMemberListStopsOnceTheOthersCommit(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x69, 0x8d, 0x11}, size: 3}
	for i := range c.size {
		tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
		slot := "slot-" + string(rune('0'+i))
		id, inc := must2(InitLog(tn.log, c.cid, slot))(t)
		tn.id = id
		c.nodes = append(c.nodes, tn)
		c.members = append(c.members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	odd := slices.Clone(c.members)
	odd[1].SPKI[0] ^= 0xff
	for k, tn := range c.nodes {
		ms := c.members
		if k == 0 {
			ms = odd
		}
		if err := Bootstrap(context.Background(), new(c.config(tn)), ms); err != nil {
			t.Fatal(err)
		}
	}
	for _, tn := range c.nodes {
		c.start(tn)
	}
	t.Cleanup(func() {
		for _, tn := range c.nodes {
			_ = tn.node.HardClose()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r, ok := c.nodes[0].node.StopReason(); ok {
			if !errors.Is(r.Err, coreconsensus.BootstrapRefused) {
				t.Fatalf("the odd founder stopped with %v, not BOOTSTRAP_REFUSED", r.Err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the odd founder never stopped: commit %d", c.nodes[0].node.Status().Commit)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, tn := range c.nodes[1:] {
		if _, ok := tn.node.StopReason(); ok {
			t.Errorf("founder %d of the majority stopped", tn.id)
		}
	}
}
