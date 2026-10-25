package consensus

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// The scenario of #548 kept as its regression: a rolling change of
// SnapshotCodec from flate to gzip, one node at a time over compacted logs.
// Each node restored with the new codec reads its own flate snapshots and
// installs the flate leader's, a leader is elected and a write commits after
// every step, and an offer naming a codec this build does not link is
// refused VERSION_UNSUPPORTED before a byte is spooled (D18). It used to end
// with every node without a table and no leader: the codec the metadata
// names was never read. The node's paths were fixed by #555 (#512), the
// offline readers by #548; this holds the whole scenario.
func TestARollingSnapshotCodecChangeKeepsALeader(t *testing.T) {
	var mu sync.Mutex
	gzip := map[coreconsensus.NodeID]bool{}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 4, 9}, every: 20, size: 3,
		configure: func(tn *testNode, cfg *Config) {
			mu.Lock()
			defer mu.Unlock()
			if gzip[tn.id] {
				cfg.SnapshotCodec = svctransform.GzipCompressor.(coretransform.BoundedDecompressor)
			}
		},
	})
	next := 0
	write := func(n int) {
		t.Helper()
		var tok coreconsensus.TokenValue
		l := c.settledLeader(10 * time.Second)
		for range n {
			tok = propose(t, l.node, fmt.Sprintf("k%d=%d", next, next))
			next++
		}
		c.waitApplied(tok.Index, 10*time.Second)
	}
	write(70)
	l := c.settledLeader(5 * time.Second)
	order := []*testNode{c.otherThan(l)}
	for _, tn := range c.nodes {
		if tn != l && tn != order[0] {
			order = append(order, tn)
		}
	}
	order = append(order, l)
	for step, tn := range order {
		c.kill(tn)
		write(50) // the others cut and compact past what tn holds
		mu.Lock()
		gzip[tn.id] = true
		mu.Unlock()
		c.start(tn)
		write(1)
		if holdsNoTable(t, tn) {
			t.Fatalf("step %d: node %d restarted on gzip holds no table", step, tn.id)
		}
		if v, _ := tn.sm.get("k5"); v != "5" {
			t.Fatalf("step %d: node %d lost its state: k5=%q", step, tn.id, v)
		}
	}
	// The offer comes from the leader the follower follows, in its term: a
	// leadership that moved between the two reads is NOT_LEADER, and the
	// offer is made again to the leader of the term that follows.
	var f *testNode
	var id string
	var acc coreconsensus.InstallAcceptValue
	for deadline := time.Now().Add(10 * time.Second); ; {
		l = c.settledLeader(5 * time.Second)
		f = c.otherThan(l)
		st := f.node.Status()
		id = fmt.Sprintf("%020d-%020d", st.Commit+100, st.Term)
		acc = f.node.offer(coreconsensus.AuthenticatedPeerValue{ID: l.id}, coreconsensus.InstallOfferValue{
			Term: st.Term, SnapshotID: id,
			Meta: coreconsensus.SnapshotMetaValue{
				ID: id, Version: 1, Log: f.node.lineage(), Codec: "unknown-codec",
				Position: coreconsensus.PositionValue{Term: st.Term, Index: st.Commit + 100},
			},
		})
		if acc.Refused != coreconsensus.CodeNotLeader || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if acc.Refused != coreconsensus.CodeVersionUnsupported {
		t.Fatalf("offer naming a codec this build does not link: %+v", acc)
	}
	f.snaps.mu.Lock()
	_, spooled := f.snaps.spools[id]
	f.snaps.mu.Unlock()
	if spooled {
		t.Fatal("an offer of an unknown codec was spooled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.settledLeader(5 * time.Second).node.Barrier(ctx); err != nil {
		t.Fatalf("no leader serves after the change: %v", err)
	}
}
