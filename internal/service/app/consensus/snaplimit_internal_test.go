package consensus

import (
	"fmt"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A state larger than MaxSnapshotBytes is never cut: the cut is refused
// SNAPSHOT_TOO_LARGE and the log is not compacted behind it. It used to be
// published, every restore of it failed on the same bound, and a rolling
// restart left every node with no table and the cluster with no leader
// (#512).
func TestNoSnapshotPastMaxSnapshotBytesIsPublished(t *testing.T) {
	const bound = 16 << 10
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 1, 2, 1}, every: 20, size: 3,
		configure: func(_ *testNode, cfg *Config) {
			cfg.SnapshotEveryBytes, cfg.MaxSnapshotBytes = 4<<10, bound
		},
	})
	value := strings.Repeat("v", 100)
	var tok coreconsensus.TokenValue
	for i := range 400 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%03d=%s%d", i, value, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	for _, tn := range c.nodes {
		cutsSettled(t, tn)
		for _, m := range must(tn.snaps.List())(t) {
			if m.DecodedSize > bound || m.EncodedSize > bound {
				t.Fatalf("node %d published %s: %d bytes decoded, %d encoded, over MaxSnapshotBytes %d",
					tn.id, m.ID, m.DecodedSize, m.EncodedSize, bound)
			}
		}
	}
	// A rolling restart: every node restores what it published, or replays.
	for i, tn := range c.nodes {
		c.kill(tn)
		c.start(tn)
		if tn.node.r.NoTable() {
			t.Fatalf("restart %d: node %d started with no table", i, tn.id)
		}
		tok = proposeOnLeader(t, c, fmt.Sprintf("restart%d=1", i))
		c.waitApplied(tok.Index, 10*time.Second)
	}
	for _, tn := range c.nodes {
		if v, _ := tn.sm.get("k399"); !strings.HasPrefix(v, value) {
			t.Fatalf("node %d lost k399 after the rolling restart: %q", tn.id, v)
		}
	}
}

// A node whose every snapshot fails its checksums, while its log still
// holds every entry from index 1, replays its log: it used to start with no
// table, and one with no leader to install it stayed so for good (#512).
func TestALogFromIndexOneIsReplayedWhenNoSnapshotRestores(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 1, 2, 2}, every: 20, size: 1,
		configure: func(_ *testNode, cfg *Config) { cfg.SnapshotTrailing = 1 << 20 },
	})
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 60 {
		tok = propose(t, tn.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	cutsSettled(t, tn)
	if first, _ := tn.log.Bounds(); first != 1 || len(must(tn.snaps.List())(t)) == 0 {
		t.Fatalf("setup: log from %d, %d snapshots", first, len(must(tn.snaps.List())(t)))
	}
	c.kill(tn)
	tn.snaps.mu.Lock()
	for _, s := range tn.snaps.done {
		s.data[len(s.data)/2] ^= 0xff
	}
	tn.snaps.mu.Unlock()
	c.start(tn)
	if tn.node.r.NoTable() {
		t.Fatal("a node holding its log from index 1 started with no table")
	}
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := tn.sm.get("k59"); v != "59" {
		t.Fatalf("the replay lost k59: %q", v)
	}
}
