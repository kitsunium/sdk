package consensus

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// refusingKV is the kv machine of a test that makes Restore fail on demand:
// on the node whose id refuse holds, with err, counting each refusal.
type refusingKV struct {
	*kv
	id      coreconsensus.NodeID
	refuse  *atomic.Uint64
	err     error
	refused *atomic.Int64
}

func (r refusingKV) Restore(rd io.Reader, m coreconsensus.SnapshotMetaValue) error {
	if coreconsensus.NodeID(r.refuse.Load()) == r.id {
		r.refused.Add(1)
		return r.err
	}
	return r.kv.Restore(rd, m)
}

// refusingCluster is a three-node cluster cutting every 20 entries whose
// machines refuse Restore with err on the node refuse names.
func refusingCluster(t *testing.T, err error) (c *cluster, refuse *atomic.Uint64, refused *atomic.Int64) {
	refuse, refused = new(atomic.Uint64), new(atomic.Int64)
	c = newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 6}, every: 20,
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = refusingKV{kv: tn.sm, id: tn.id, refuse: refuse, err: err, refused: refused}
		},
	})
	return c, refuse, refused
}

// lagBehindAnInstall stops f, isolated, while the leader writes n entries
// and every live log compacts past what f holds: at its restart only an
// install catches it up. It returns the last token written.
func lagBehindAnInstall(t *testing.T, c *cluster, f *testNode, n int) coreconsensus.TokenValue {
	t.Helper()
	c.net.Isolate(f.id, true)
	c.kill(f)
	var tok coreconsensus.TokenValue
	for i := range n {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, 10)
	return tok
}

// holdOnlyTheLeadersNewest leaves f's stopped store holding one snapshot,
// the leader's newest, its bytes flipped so its checksums fail: the state of
// a node that joined by installing it and has not cut since, whose copy then
// rotted. It returns that snapshot's id.
func holdOnlyTheLeadersNewest(t *testing.T, l, f *testNode) string {
	t.Helper()
	newest := leaderSnapshots(t, l)[0]
	l.snaps.mu.Lock()
	src := l.snaps.done[newest.ID]
	data := bytes.Clone(src.data)
	l.snaps.mu.Unlock()
	data[len(data)/2] ^= 0xff
	f.snaps.mu.Lock()
	f.snaps.done = map[string]*memSnap{newest.ID: {meta: newest, data: data}}
	f.snaps.mu.Unlock()
	return newest.ID
}

// A node with no table keeps the snapshot its members are read from until
// a replacement is published: the offer of the id it holds used to remove
// its copy first, and a replacement that then failed — a cut transfer, a
// Restore refused — left it no snapshot. At its next start its log began
// past a compaction with nothing naming the members: it admitted nobody and
// was isolated for good (#526).
func TestANodeWithNoTableKeepsItsMembersThroughAFailedReplacement(t *testing.T) {
	c, refuse, refused := refusingCluster(t, errors.New("this build does not read that state"))
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	tok := lagBehindAnInstall(t, c, f, 120)
	c.start(f)
	c.net.Isolate(f.id, false)
	c.waitApplied(tok.Index, 10*time.Second)

	// The installed copy rots; the node restarts with no table and every
	// replacement fails its Restore.
	c.net.Isolate(f.id, true)
	c.kill(f)
	held := holdOnlyTheLeadersNewest(t, l, f)
	refuse.Store(uint64(f.id))
	c.start(f)
	if !f.node.r.NoTable() {
		t.Fatal("setup: the node started with a table")
	}
	c.net.Isolate(f.id, false)
	// An entry past the base the node holds: the append it is sent is
	// answered with a request for a snapshot.
	proposeOnLeader(t, c, "nudge=1")
	deadline := time.Now().Add(10 * time.Second)
	for refused.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("no replacement reached Restore twice (refused %d)", refused.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.net.Isolate(f.id, true)
	c.kill(f)
	if metas := must(f.snaps.List())(t); len(metas) == 0 {
		t.Errorf("the node with no table was left no snapshot (it held %s)", held)
	}

	// The cause is gone: the node restarts and must be fed.
	refuse.Store(0)
	c.start(f)
	c.net.Isolate(f.id, false)
	if m := f.node.Membership(); len(m.Members) == 0 {
		t.Errorf("the node restarted with no member in its configuration")
	}
	tok = proposeOnLeader(t, c, "after=1")
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := f.sm.get("k5"); v != "5" {
		t.Fatalf("the node was never installed again: k5=%q", v)
	}
}

// A node left no snapshot, whose log begins past a compaction with no
// configuration entry after it, has nothing that names its members: it is
// refused at start, with the reason, instead of running with no member to
// admit and no way to be installed again (#526).
func TestANodeWithNothingNamingItsMembersIsRefused(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var tok coreconsensus.TokenValue
	for i := range 80 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if first, _ := f.log.Bounds(); first > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the follower never compacted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.kill(f)
	f.snaps.mu.Lock()
	f.snaps.done = map[string]*memSnap{}
	f.snaps.mu.Unlock()
	if n, err := NewNode(c.config(f)); !errors.Is(err, coreconsensus.SnapshotCorrupt) {
		if n != nil {
			_ = n.HardClose()
		}
		t.Fatalf("a node with nothing naming its members started: %v", err)
	}
}
