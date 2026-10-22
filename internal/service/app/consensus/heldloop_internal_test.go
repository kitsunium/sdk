package consensus

import (
	"bytes"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// laggingWithOwnSnapshots is a three-node cluster cutting every 20 entries
// whose follower f took snapshots of its own, then stopped, isolated, while
// the leader wrote 200 entries and every live log compacted past it. It
// returns the leader, f, and the leader's newest snapshot.
func laggingWithOwnSnapshots(t *testing.T) (*cluster, *testNode, *testNode, coreconsensus.SnapshotMetaValue) {
	t.Helper()
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var tok coreconsensus.TokenValue
	for i := range 60 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("a%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	waitFor(t, "a snapshot of the follower's own", func() bool { return len(must(f.snaps.List())(t)) > 0 })
	lagBehindAnInstall(t, c, f, 200)
	l = c.leader(5 * time.Second)
	cutsSettled(t, l)
	return c, l, f, leaderSnapshots(t, l)[0]
}

// publish puts snapshot m, with data, in f's stopped store, as an install
// leaves it after its commit.
func publish(f *testNode, m coreconsensus.SnapshotMetaValue, data []byte) {
	f.snaps.mu.Lock()
	f.snaps.done[m.ID] = &memSnap{meta: m, data: data}
	f.snaps.mu.Unlock()
}

// leaderCopy is the bytes of the leader's snapshot id.
func leaderCopy(l *testNode, id string) []byte {
	l.snaps.mu.Lock()
	defer l.snaps.mu.Unlock()
	return bytes.Clone(l.snaps.done[id].data)
}

// caughtUp requires f to apply the leader's commit soon. Nothing is
// written meanwhile: a newer cut on the leader would end a loop on x by
// itself, so the leader's newest must still be x at the end.
func caughtUp(t *testing.T, c *cluster, l, f *testNode, x coreconsensus.SnapshotMetaValue) {
	t.Helper()
	c.net.Isolate(f.id, false)
	c.waitApplied(l.node.Status().Commit, 5*time.Second)
	if v, _ := f.sm.get("k150"); v != "150" {
		t.Fatalf("the lagging node applied the commit without the state: k150=%q", v)
	}
	if newest := leaderSnapshots(t, l)[0]; newest.ID != x.ID {
		t.Fatalf("setup: the leader cut %s past %s, which ends a loop by itself", newest.ID, x.ID)
	}
}

// A published snapshot that fails to restore at a start is unpublished
// once an older one restores: left listed, the node — started with a table
// from its own older cut — answered the leader's offer of that id as held,
// the leader moved it to probe past it, the append was refused, and the
// offer came again, for as long as that snapshot stayed the leader's newest
// (#528).
func TestASnapshotThatDidNotRestoreIsUnpublished(t *testing.T) {
	c, l, f, x := laggingWithOwnSnapshots(t)
	data := leaderCopy(l, x.ID)
	data[len(data)/2] ^= 0xff
	publish(f, x, data)
	c.start(f)
	if f.node.r.NoTable() {
		t.Fatal("setup: the lagging node started with no table")
	}
	for _, m := range must(f.snaps.List())(t) {
		if m.ID == x.ID {
			t.Errorf("the snapshot that failed its checksums is still published")
		}
	}
	caughtUp(t, c, l, f, x)
}

// A snapshot that did not restore for a reason of this node's — a disk
// failure, a MaxSnapshotBytes lowered since — stays published: it may
// restore at the next start, and the log may have been compacted to it.
func TestASnapshotOverALoweredLimitStaysPublished(t *testing.T) {
	var limit atomic.Int64
	limit.Store(1 << 30)
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 8}, every: 20,
		configure: func(_ *testNode, cfg *Config) { cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes = limit.Load(), 1 },
	})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var tok coreconsensus.TokenValue
	for i := range 60 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%s", i, bytes.Repeat([]byte("v"), 200)))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	cutsSettled(t, f)
	metas := must(f.snaps.List())(t)
	if len(metas) < 2 || max(metas[0].EncodedSize, metas[0].DecodedSize) <= max(metas[1].EncodedSize, metas[1].DecodedSize) {
		t.Fatalf("setup: the follower holds no older, smaller snapshot: %+v", metas)
	}
	c.net.Isolate(f.id, true)
	c.kill(f)
	limit.Store((max(metas[0].EncodedSize, metas[0].DecodedSize) + max(metas[1].EncodedSize, metas[1].DecodedSize)) / 2)
	c.start(f)
	if f.node.r.NoTable() {
		t.Fatal("setup: the follower started with no table")
	}
	held := false
	for _, m := range must(f.snaps.List())(t) {
		held = held || m.ID == metas[0].ID
	}
	if !held {
		t.Fatalf("the snapshot over the lowered limit %d was unpublished", limit.Load())
	}
}

// A snapshot that did not restore is unpublished only once the older one
// restored in its place is known to continue the log: a log compacted past
// the older one cannot start from it, and with the newer one gone no start
// could ever succeed again.
func TestNoSnapshotIsUnpublishedBeforeItsFallbackContinuesTheLog(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var tok coreconsensus.TokenValue
	for i := range 60 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	cutsSettled(t, f)
	metas := must(f.snaps.List())(t)
	if len(metas) < 2 {
		t.Fatalf("setup: the follower holds %d snapshots", len(metas))
	}
	c.net.Isolate(f.id, true)
	c.kill(f)
	// The log compacted to the newer snapshot, whose copy then rots.
	if err := f.log.CompactPrefix(metas[0].Position.Index); err != nil {
		t.Fatal(err)
	}
	f.snaps.mu.Lock()
	f.snaps.done[metas[0].ID].data[len(f.snaps.done[metas[0].ID].data)/2] ^= 0xff
	f.snaps.mu.Unlock()
	f.sm = newKV()
	if _, err := NewNode(c.config(f)); err == nil {
		t.Fatal("setup: a node started from a snapshot its log does not continue")
	}
	held := false
	for _, m := range must(f.snaps.List())(t) {
		held = held || m.ID == metas[0].ID
	}
	if !held {
		t.Fatal("the newer snapshot was unpublished though the start that fell back failed")
	}
}

// A node answers held only for a snapshot it holds at or below what it
// applied: one it holds past that — published, never restored — is not
// its state, and the offer replaces it (#528).
func TestAHeldSnapshotPastTheAppliedIndexIsReplaced(t *testing.T) {
	c, l, f, x := laggingWithOwnSnapshots(t)
	c.start(f)
	if f.node.r.NoTable() {
		t.Fatal("setup: the lagging node started with no table")
	}
	// Published while the node runs past what it applied: the copy an
	// install left whose restore did not take.
	publish(f, x, leaderCopy(l, x.ID))
	caughtUp(t, c, l, f, x)
}
