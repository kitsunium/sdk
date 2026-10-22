package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
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

// A snapshot whose state the machine refuses as VERSION_UNSUPPORTED — a
// format this build does not read — stops the node, as an unknown entry
// kind does (D18): it used to be removed and answered as a failure, the
// leader streamed it again at every probe, and the node silently left the
// quorum while it reported ready. The copy is kept, and a restart refuses
// to open on it rather than fall back to no table (#513).
func TestASnapshotOfAnUnknownFormatStopsTheNode(t *testing.T) {
	c, refuse, refused := refusingCluster(t, origin(coreconsensus.VersionUnsupported, errs.String("why", "state format 2")))
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	lagBehindAnInstall(t, c, f, 120)
	refuse.Store(uint64(f.id))
	c.start(f)
	c.net.Isolate(f.id, false)
	deadline := time.Now().Add(10 * time.Second)
	for !f.node.stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("the node kept running after %d refused restores", refused.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r, ok := f.node.StopReason(); !ok || !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("stop reason: %+v", r)
	}
	if got := refused.Load(); got != 1 {
		t.Fatalf("the node restored %d snapshots it cannot read, want 1", got)
	}
	c.kill(f)
	if metas := must(f.snaps.List())(t); len(metas) == 0 {
		t.Fatal("the snapshot the node cannot read was removed")
	}
	f.sm = newKV()
	if n, err := NewNode(c.config(f)); !errors.Is(err, coreconsensus.VersionUnsupported) {
		if n != nil {
			_ = n.HardClose()
		}
		t.Fatalf("a restart over a snapshot this build cannot read: %v", err)
	}
}

// An offer whose snapshot metadata is of a version this node does not know
// stops the node, after refusing it (D18, #513).
func TestAnOfferOfAnUnknownSnapshotVersionStopsTheNode(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	st := f.node.Status()
	id := fmt.Sprintf("%020d-%020d", st.Commit+100, st.Term)
	acc := f.node.offer(coreconsensus.AuthenticatedPeerValue{ID: l.id}, coreconsensus.InstallOfferValue{
		Term: st.Term, SnapshotID: id,
		Meta: coreconsensus.SnapshotMetaValue{
			ID: id, Version: 2, Log: f.node.lineage(),
			Position: coreconsensus.PositionValue{Term: st.Term, Index: st.Commit + 100},
		},
	})
	if acc.Refused != coreconsensus.CodeVersionUnsupported {
		t.Fatalf("offer of a version 2 snapshot: %+v", acc)
	}
	if !f.node.stopped.Load() {
		t.Fatal("the node kept running after a snapshot version it does not know")
	}
	if r, ok := f.node.StopReason(); !ok || !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("stop reason: %+v", r)
	}
}

// A snapshot is decoded with the codec its metadata names: a node whose
// SnapshotCodec changed between two starts reads the snapshots it cut
// before, where it used to fail every one of them and start with no table.
// A codec this build does not know refuses the start, VERSION_UNSUPPORTED
// (D18 "Compression", #512).
func TestASnapshotIsReadWithTheCodecItWasWrittenWith(t *testing.T) {
	var gzip atomic.Bool
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 1, 2}, every: 20, size: 1,
		configure: func(_ *testNode, cfg *Config) {
			if gzip.Load() {
				cfg.SnapshotCodec = svctransform.GzipCompressor.(coretransform.BoundedDecompressor)
			}
		},
	})
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 80 {
		tok = propose(t, tn.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if first, _ := tn.log.Bounds(); first > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the node never compacted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.kill(tn)
	gzip.Store(true)
	c.start(tn)
	if tn.node.r.NoTable() {
		t.Fatal("a node whose codec changed started with no table")
	}
	if v, _ := tn.sm.get("k5"); v != "5" {
		t.Fatalf("the snapshot was not restored: k5=%q", v)
	}
	c.kill(tn)
	tn.snaps.mu.Lock()
	for _, s := range tn.snaps.done {
		s.meta.Codec = "unknown-codec"
	}
	tn.snaps.mu.Unlock()
	tn.sm = newKV()
	if n, err := NewNode(c.config(tn)); !errors.Is(err, coreconsensus.VersionUnsupported) {
		if n != nil {
			_ = n.HardClose()
		}
		t.Fatalf("a start over a snapshot of an unknown codec: %v", err)
	}
}

// cutsSettled waits until tn has no cut in flight and none coalesced, five
// checks in a row: the snapshot a test then reads is the newest it keeps.
func cutsSettled(t *testing.T, tn *testNode) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for quiet := 0; quiet < 5; {
		ss := &tn.node.snapState
		ss.mu.Lock()
		busy := ss.inFlight || ss.pending
		ss.mu.Unlock()
		if busy {
			quiet = 0
		} else {
			quiet++
		}
		if time.Now().After(deadline) {
			t.Fatal("the node's cuts never settled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// streamingTransport is a node's end of a memory network that counts the
// snapshot streams the node sends, and those its peer refused as corrupt.
type streamingTransport struct {
	*MemoryTransport
	streams, corrupt atomic.Int64
}

// Stream implements Transport, counting the stream and its verdict.
func (t *streamingTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	t.streams.Add(1)
	err := t.MemoryTransport.Stream(ctx, to, offer, open)
	if errors.Is(err, coreconsensus.SnapshotCorrupt) {
		t.corrupt.Add(1)
	}
	return err
}

// A leader whose newest snapshot rotted on its disk checks its own copy
// when a peer refuses it as corrupt, removes it and cuts a new one: it used
// to stream it again at every probe, never reading it back, and the peer
// stayed out of replication until the leader's next cut, SnapshotEvery
// entries later (#525).
func TestALeaderReplacesASnapshotItsPeerFindsCorrupt(t *testing.T) {
	var mu sync.Mutex
	streaming := map[coreconsensus.NodeID]*streamingTransport{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 5}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		st := streaming[tn.id]
		if st == nil {
			st = &streamingTransport{MemoryTransport: NewMemoryTransport(c.net, tn.id)}
			streaming[tn.id] = st
		}
		cfg.Transport = st
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	tok := lagBehindAnInstall(t, c, f, 200)
	c.waitApplied(tok.Index, 10*time.Second)
	l = c.leader(5 * time.Second)
	cutsSettled(t, l)
	newest := leaderSnapshots(t, l)[0]
	l.snaps.mu.Lock()
	bad := l.snaps.done[newest.ID].data
	bad[len(bad)/2] ^= 0xff
	l.snaps.mu.Unlock()
	mu.Lock()
	lt := streaming[l.id]
	mu.Unlock()
	c.start(f)
	c.net.Isolate(f.id, false)
	// No write follows: only the leader's own repair lets the peer in.
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := f.sm.get("k199"); v != "199" {
		t.Fatalf("the lagging node: k199=%q", v)
	}
	if got := lt.corrupt.Load(); got > 1 {
		t.Fatalf("the leader streamed a snapshot its peer found corrupt %d times", got)
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
