package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// diskNode is a node over the shipped layout.
type diskNode struct {
	dir  string
	dd   DataDir
	id   coreconsensus.NodeID
	sm   *kv
	node *Node
}

func diskConfig(c *cluster, dn *diskNode) Config {
	return Config{
		Cluster: c.cid, Machine: dn.sm, Durability: DurabilityVolatile, Log: dn.dd.Log(), Snapshots: dn.dd.Snapshots(),
		Transport: NewMemoryTransport(c.net, dn.id), InitialTiming: testTimingDisk(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 3,
		SnapshotEvery: 50, SnapshotTrailing: 10,
	}
}

func (dn *diskNode) open(t *testing.T, c *cluster) {
	t.Helper()
	dn.dd = openDir(t, dn.dir, 16<<10)
	dn.sm = newKV()
	n, err := NewNode(diskConfig(c, dn))
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	dn.node = n
}

// crash cuts the node the way a power loss would, then releases what it
// held. Each step is reported rather than ignored: a directory still in use
// after HardClose is exactly the leak R7-11 bounds, and the restart that
// follows would then test the wrong thing.
func (dn *diskNode) crash(t *testing.T, c *cluster) {
	t.Helper()
	if err := dn.node.HardClose(); err != nil {
		t.Errorf("hard close %d: %v", dn.id, err)
	}
	if err := NewMemoryTransport(c.net, dn.id).Close(); err != nil {
		t.Errorf("close transport %d: %v", dn.id, err)
	}
	if err := dn.dd.Close(); err != nil {
		t.Errorf("close data directory %d: %v", dn.id, err)
	}
	dn.node = nil
}

// TestDiskClusterSurvivesFullRestart runs three nodes over real segmented
// logs and snapshot stores, with rotation, snapshots and compaction, then
// cuts all three at once and restarts them.
func TestDiskClusterSurvivesFullRestart(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7}}
	var nodes []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = openDir(t, dn.dir, 16<<10)
		slot := fmt.Sprintf("d%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(diskConfig(c, dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.open(t, c)
	}
	t.Cleanup(func() {
		for _, dn := range nodes {
			if dn.node != nil {
				dn.crash(t, c)
			}
		}
	})
	leader := func() *diskNode {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, dn := range nodes {
				if dn.node != nil && dn.node.Status().Role == coreconsensus.RoleLeader {
					return dn
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("no leader")
		return nil
	}
	var tok coreconsensus.TokenValue
	for i := range 300 {
		tok = propose(t, leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	// A data directory a live node holds refuses offline work.
	if _, err := Inspect(nodes[0].dd); err == nil {
		t.Fatal("Inspect on a live directory")
	}
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	for _, dn := range nodes {
		dn.open(t, c)
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, dn := range nodes {
		for {
			if v, _ := dn.sm.get("k299"); v == "v299" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("node %d lost k299 after the restart (status %+v)", dn.id, dn.node.Status())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	tok2 := propose(t, leader().node, "after=1")
	if tok2.Index <= tok.Index {
		t.Fatalf("index went back: %d then %d", tok.Index, tok2.Index)
	}
	// A cut the restart's replay started may still be between publishing
	// its snapshot and removing the oldest: the count is waited for, on a
	// bound, rather than read once (#304). A third snapshot that stays is a
	// retention that never ran — an install used to leave one.
	for _, dn := range nodes {
		deadline := time.Now().Add(5 * time.Second)
		metas := must(dn.dd.Snapshots().List())(t)
		for len(metas) > 2 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
			metas = must(dn.dd.Snapshots().List())(t)
		}
		if len(metas) == 0 || len(metas) > 2 {
			t.Fatalf("node %d keeps %d snapshots", dn.id, len(metas))
		}
		if f, _ := dn.dd.Log().Bounds(); f < 100 {
			t.Fatalf("node %d log not compacted: first %d", dn.id, f)
		}
	}
}

// testTimingDisk is slower than the memory clusters' timing: a real fsync,
// FlushFileBuffers on Windows above all, can take longer than a 10 ms
// heartbeat, and elections would churn under it.
func testTimingDisk() coreconsensus.TimingContractSpec {
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 50 * time.Millisecond,
		ElectionTimeoutMin: 500 * time.Millisecond, ElectionTimeoutMax: time.Second, MaxClockDrift: 0.05,
	}
}
