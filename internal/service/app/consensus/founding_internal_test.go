package consensus

import (
	"context"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheFoundingContractIsTheOneNewNodeValidates is #523: Bootstrap wrote
// the raw InitialTiming as the founding contract, which rules on every node
// once entry 1 commits, while NewNode completed and validated its own copy.
// A drift alone became TimingDefaults with no drift, under which a lease is
// the whole election window; an unset maximum ruled as 0 and refused every
// promotion; an unset minimum was refused by the engine, and each node ran
// generation 1 on its own InitialTiming again (#421). The contract in force
// on every node is now exactly the one NewNode validated.
func TestTheFoundingContractIsTheOneNewNodeValidates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		timing coreconsensus.TimingContractSpec
		reads  ReadMode
	}{
		{"drift only", coreconsensus.TimingContractSpec{Generation: 1, MaxClockDrift: 0.05}, ReadLease},
		{"maximum unset", coreconsensus.TimingContractSpec{Generation: 1, HeartbeatInterval: 10 * time.Millisecond, ElectionTimeoutMin: 100 * time.Millisecond}, ReadIndex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 3}, size: 3, timing: tc.timing}
			c.configure = func(_ *testNode, cfg *Config) { cfg.Reads = tc.reads }
			newClusterWith(t, c)
			for _, tn := range c.nodes {
				want := must(withDefaults(new(c.config(tn))))(t).InitialTiming
				if got := tn.node.Status().Timing; got != want {
					t.Fatalf("node %d runs generation 1 as %+v; NewNode validated %+v", tn.id, got, want)
				}
			}
		})
	}
}

// TestAFoundingContractWithoutItsMinimumRulesEveryNode is #523's third
// case: a founding InitialTiming with no minimum was written as it was, the
// engine refused it, and every node ran generation 1 on its own
// InitialTiming, which may differ from node to node (#421). Completed by
// Bootstrap, it rules every node alike, whatever each one started with.
func TestAFoundingContractWithoutItsMinimumRulesEveryNode(t *testing.T) {
	partial := coreconsensus.TimingContractSpec{Generation: 1, HeartbeatInterval: 10 * time.Millisecond}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 5}, size: 3, timing: partial}
	newClusterWith(t, c)
	for _, tn := range c.nodes {
		c.kill(tn)
	}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.InitialTiming.ElectionTimeoutMin = time.Duration(slices.Index(c.nodes, tn)+3) * 100 * time.Millisecond
	}
	for _, tn := range c.nodes {
		c.start(tn)
	}
	want := partial
	clampTiming(&want)
	for _, tn := range c.nodes {
		if got := tn.node.Status().Timing; got != want {
			t.Fatalf("node %d runs generation 1 as %+v; the founding contract is %+v", tn.id, got, want)
		}
	}
}

// TestBootstrapRefusesATimingNewNodeRefuses: a founding contract NewNode
// would refuse is refused by Bootstrap before anything is written, rather
// than ruling every node from entry 1.
func TestBootstrapRefusesATimingNewNodeRefuses(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 4}}
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, inc, err := InitLog(tn.log, c.cid, "slot-0")
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	c.nodes = []*testNode{tn}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "slot-0", SPKI: MemorySPKI(id), Voter: true}}
	cfg := c.config(tn)
	cfg.MinVoters, cfg.Reads = 1, ReadLease
	cfg.InitialTiming = coreconsensus.TimingContractSpec{Generation: 1}
	err = Bootstrap(context.Background(), &cfg, members)
	if !errs.HasCode(err, coreconsensus.CodeConsensusMisconfigured) {
		t.Fatalf("Bootstrap of a ReadLease cluster with no drift: %v, want CONSENSUS_MISCONFIGURED", err)
	}
	if first, last := tn.log.Bounds(); last >= first {
		t.Fatalf("the refused Bootstrap wrote entries [%d, %d]", first, last)
	}
}
