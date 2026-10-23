package consensus

import (
	"bytes"
	"errors"
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

// loweredLimitCluster is a three-node cluster that cuts at every applied
// byte, under a MaxSnapshotBytes the test can lower before a restart.
func loweredLimitCluster(t *testing.T, limit *atomic.Int64) *cluster {
	t.Helper()
	limit.Store(1 << 30)
	return newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 8}, every: 20,
		configure: func(_ *testNode, cfg *Config) { cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes = limit.Load(), 1 },
	})
}

// writeInStep writes n values of 200 bytes, each applied by f before the
// next is proposed. The leader cuts at every write and compacts its log to
// SnapshotTrailing entries before its older snapshot: a follower further
// behind than that was caught up by an install, which compacts its log to
// the snapshot installed, and its own older snapshot no longer continued
// the log (#574).
func writeInStep(t *testing.T, c *cluster, f *testNode, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		tok := proposeOnLeader(t, c, fmt.Sprintf("k%d=%s", i, bytes.Repeat([]byte("v"), 200)))
		waitAppliedOn(t, f, tok.Index)
	}
}

// waitAppliedOn waits until tn applied index.
func waitAppliedOn(t *testing.T, tn *testNode, index uint64) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); tn.node.Status().Applied < index; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("node %d applied %d < %d", tn.id, tn.node.Status().Applied, index)
		}
	}
}

// A snapshot that did not restore for a reason of this node's — a disk
// failure, a MaxSnapshotBytes lowered since — stays published: it may
// restore at the next start, and the log may have been compacted to it.
func TestASnapshotOverALoweredLimitStaysPublished(t *testing.T) {
	var limit atomic.Int64
	c := loweredLimitCluster(t, &limit)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	writeInStep(t, c, f, 0, 60)
	// The follower is cut off before its snapshots are read: an election's
	// entry reaching it after, then its cut, would remove the older one.
	// The two newest hold the same state when such an entry came after the
	// last write: one more write, and they differ again.
	var metas []coreconsensus.SnapshotMetaValue
	for i := 60; ; i++ {
		c.net.Isolate(f.id, true)
		cutsSettled(t, f)
		metas = must(f.snaps.List())(t)
		if len(metas) >= 2 && snapshotSize(&metas[0]) > snapshotSize(&metas[1]) {
			break
		}
		if i == 70 {
			t.Fatalf("setup: the follower holds no older, smaller snapshot: %+v", metas)
		}
		c.net.Isolate(f.id, false)
		writeInStep(t, c, f, i, 1)
	}
	if first, _ := f.log.Bounds(); first > metas[1].Position.Index+1 {
		t.Fatalf("setup: the follower's log starts at %d, past its older snapshot %s", first, metas[1].ID)
	}
	c.kill(f)
	limit.Store((snapshotSize(&metas[0]) + snapshotSize(&metas[1])) / 2)
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

// installedPastItsOwn catches f up by an install, then cuts it off, and
// returns its snapshots: the installed one, newest, larger than f's own
// older one, and f's log continuing from the installed one alone. A cut
// slower than the writes may leave a live node's log uncompacted past f,
// and an entry of an election after the install makes f cut a snapshot of
// its own past it: both are tried again.
func installedPastItsOwn(t *testing.T, c *cluster, f *testNode) []coreconsensus.SnapshotMetaValue {
	t.Helper()
	for i, try := 10, 0; ; try++ {
		c.net.Isolate(f.id, true)
		cutsSettled(t, f)
		_, fLast := f.log.Bounds()
		var tok coreconsensus.TokenValue
		for range 30 {
			tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%s", i, bytes.Repeat([]byte("v"), 200)))
			i++
		}
		for !othersCompactedPast(t, c, f, tok.Index, fLast+1) {
			if i > 400 {
				t.Fatalf("setup: the live nodes never compacted past %d", fLast+1)
			}
			tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%s", i, bytes.Repeat([]byte("v"), 200)))
			i++
		}
		c.net.Isolate(f.id, false)
		waitAppliedOn(t, f, tok.Index)
		c.net.Isolate(f.id, true)
		cutsSettled(t, f)
		metas := must(f.snaps.List())(t)
		first, _ := f.log.Bounds()
		if len(metas) >= 2 && first > metas[1].Position.Index+1 && first <= metas[0].Position.Index+1 &&
			snapshotSize(&metas[0]) > snapshotSize(&metas[1]) {
			return metas
		}
		if try == 5 {
			t.Fatalf("setup: the follower's log [%d, ...] never continued from an installed snapshot alone: %+v", first, metas)
		}
	}
}

// othersCompactedPast reports, once every node but f applied index and
// settled its cuts, whether each one's log starts past past.
func othersCompactedPast(t *testing.T, c *cluster, f *testNode, index, past uint64) bool {
	t.Helper()
	for _, tn := range c.nodes {
		if tn == f {
			continue
		}
		waitAppliedOn(t, tn, index)
		cutsSettled(t, tn)
		if first, _ := tn.log.Bounds(); first <= past {
			return false
		}
	}
	return true
}

// A start whose log continues only from a snapshot over a lowered
// MaxSnapshotBytes is refused SNAPSHOT_TOO_LARGE, and the snapshot stays
// published. An install compacts the log to the snapshot it installs, past
// the node's own older one; the start fell back to that one and was refused
// LOG_CORRUPT, which names a damage the log does not have and calls for a
// wipe, where raising the limit back was enough (#574).
func TestAStartOverALoweredLimitAfterAnInstallSaysSo(t *testing.T) {
	var limit atomic.Int64
	c := loweredLimitCluster(t, &limit)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	writeInStep(t, c, f, 0, 10)
	metas := installedPastItsOwn(t, c, f)
	c.kill(f)
	limit.Store((snapshotSize(&metas[0]) + snapshotSize(&metas[1])) / 2)
	f.sm = newKV()
	n, err := NewNode(c.config(f))
	if err == nil {
		_ = n.HardClose()
	}
	if !errors.Is(err, coreconsensus.SnapshotTooLarge) {
		t.Fatalf("a start whose log continues from a snapshot over the lowered limit: %v", err)
	}
	held := false
	for _, m := range must(f.snaps.List())(t) {
		held = held || m.ID == metas[0].ID
	}
	if !held {
		t.Fatal("the installed snapshot over the lowered limit was unpublished")
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
