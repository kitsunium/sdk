package consensus

import (
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// installInterrupted runs three nodes on disk, has the lagging one catch up
// by an install that crashes right after its snapshot is published (D7's
// commit point), and stops every node: the lagging one's newest snapshot
// lies past the end of its log. It returns the cluster, the nodes and the
// lagging one, its directory open.
func installInterrupted(t *testing.T) (*cluster, []*diskNode, *diskNode) {
	t.Helper()
	c, nodes := threeOnDisk(t, 3)
	lead := leaderOf(t, nodes)
	lag := nodes[0]
	if lag == lead {
		lag = nodes[1]
	}
	_, lagLast := lag.dd.Log().Bounds()
	var live []*diskNode
	for _, dn := range nodes {
		if dn != lag {
			live = append(live, dn)
		}
	}
	c.net.Isolate(lag.id, true)
	lag.crash(t, c)
	for i := range 200 {
		proposeOnDisk(t, live, fmt.Sprintf("k%d=v%d", i, i))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if first, _ := leaderOf(t, live).dd.Log().Bounds(); first > lagLast+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the leader never compacted past %d", lagLast)
		}
		time.Sleep(10 * time.Millisecond)
	}
	lag.open(t, c)
	lag.node.crashAfterPublish.Store(true)
	c.net.Isolate(lag.id, false)
	for deadline = time.Now().Add(10 * time.Second); !lag.node.stopped.Load(); {
		if time.Now().After(deadline) {
			t.Fatalf("no install reached publication: the node applied %d", lag.node.Status().Applied)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	lag.dd = openDir(t, lag.dir, 16<<10)
	metas := must(lag.dd.Snapshots().List())(t)
	if _, last := lag.dd.Log().Bounds(); len(metas) == 0 || metas[0].Position.Index <= last {
		t.Fatalf("setup: no snapshot published past the log's end %d (%d snapshots)", last, len(metas))
	}
	return c, nodes, lag
}

// A node that crashed inside an install, its snapshot published past the
// end of its log, is the survivor a Recover keeps: Recover finishes the
// install first, so the forced entry follows the snapshot. It used to follow
// the short log, and the next start, finishing the install, erased it: the
// node fell back to the snapshot's old voters and never led again (#506).
// Inspect reports the index the snapshot holds, not the log's alone.
func TestRecoverFinishesAnInterruptedInstall(t *testing.T) {
	c, nodes, keep := installInterrupted(t)
	snapIdx := must(keep.dd.Snapshots().List())(t)[0].Position.Index
	r := must(Inspect(keep.dd))(t)
	if r.Last < snapIdx {
		t.Errorf("Inspect reports Last %d below the snapshot the directory holds at %d", r.Last, snapIdx)
	}
	next := coreconsensus.ClusterID{7, 5, 6}
	members, opt := recoverArgs(t, c, nodes, keep, next)
	if err := Recover(keep.dd, next, members, opt); err != nil {
		t.Fatal(err)
	}
	if first, last := keep.dd.Log().Bounds(); last != snapIdx+1 || first != snapIdx+1 {
		t.Errorf("after Recover the log is [%d,%d], want the forced entry alone at %d", first, last, snapIdx+1)
	}
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keep.node = n
	defer keep.crash(t, c)
	after := propose(t, n, "after=recover")
	if after.Cluster != next || after.Index <= snapIdx {
		t.Fatalf("the recovered node wrote at %v, want past the snapshot at %d in %s", after, snapIdx, next)
	}
	if m := n.Membership(); len(m.Members) != 1 || m.Members[0].ID != keep.id {
		t.Fatalf("the recovered node runs the configuration %+v, not the forced one", m)
	}
	if v, _ := keep.sm.get("k0"); v != "v0" {
		t.Fatalf("the recovered state lost what the snapshot holds: k0=%q", v)
	}
}
