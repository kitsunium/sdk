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
	return Config{Cluster: c.cid, Machine: dn.sm, Durability: DurabilityVolatile, Log: dn.dd.Log(), Snapshots: dn.dd.Snapshots(),
		Transport: c.net.Transport(dn.id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 3,
		SnapshotEvery: 50, SnapshotTrailing: 10}
}

func (dn *diskNode) open(t *testing.T, c *cluster) {
	t.Helper()
	dn.dd = openDir(t, dn.dir, 16<<10)
	dn.sm = newKV()
	n, err := NewRaft(diskConfig(c, dn))
	if err != nil {
		t.Fatalf("NewRaft: %v", err)
	}
	dn.node = n
}

func (dn *diskNode) crash(c *cluster) {
	_ = dn.node.HardClose()
	_ = c.net.Transport(dn.id).Close()
	_ = dn.dd.Close()
	dn.node = nil
}

// TestDiskClusterSurvivesFullRestart runs three nodes over real segmented
// logs and snapshot stores, with rotation, snapshots and compaction, then
// cuts all three at once and restarts them.
func TestDiskClusterSurvivesFullRestart(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7}}
	var nodes []*diskNode
	var members []coreconsensus.Member
	for i := 0; i < 3; i++ {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = openDir(t, dn.dir, 16<<10)
		slot := fmt.Sprintf("d%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.Member{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), diskConfig(c, dn), members); err != nil {
			t.Fatal(err)
		}
		_ = dn.dd.Close()
		dn.open(t, c)
	}
	t.Cleanup(func() {
		for _, dn := range nodes {
			if dn.node != nil {
				dn.crash(c)
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
	var tok coreconsensus.Token
	for i := 0; i < 300; i++ {
		tok = propose(t, leader().node, fmt.Sprintf("k%d=v%d", i, i))
	}
	// A data directory a live node holds refuses offline work.
	if _, err := Inspect(nodes[0].dd); err == nil {
		t.Fatal("Inspect on a live directory")
	}
	for _, dn := range nodes {
		dn.crash(c)
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
	for _, dn := range nodes {
		if metas, _ := dn.dd.Snapshots().List(); len(metas) == 0 || len(metas) > 2 {
			t.Fatalf("node %d keeps %d snapshots", dn.id, len(metas))
		}
		if f, _ := dn.dd.Log().Bounds(); f < 100 {
			t.Fatalf("node %d log not compacted: first %d", dn.id, f)
		}
	}
}
