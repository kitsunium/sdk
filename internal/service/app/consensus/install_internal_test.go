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
//
// The wait is past the entry after f's end, which stopBehind reads once
// the frames queued for f are dropped, never past a constant: f stops
// holding whatever the cluster wrote before, near 63 under
// laggingWithOwnSnapshots, and a live log still covering that end fed it
// by appends (#1577).
func lagBehindAnInstall(t *testing.T, c *cluster, f *testNode, n int) coreconsensus.TokenValue {
	t.Helper()
	end := stopBehind(c, f)
	var tok coreconsensus.TokenValue
	for i := range n {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, end+1)
	return tok
}

// #1577: lagBehindAnInstall waited until every live log started past 10, a
// constant, while the node it stopped had applied what the cluster wrote
// before: laggingWithOwnSnapshots stops it near 63. Returned with a live
// log still covering that end, the restarted node caught up by appends and
// the install its callers name never ran. The live nodes' cuts are held
// here from before the call, so their logs start where the writes before
// it left them, past 10 and at or below the stopped node's end: the helper
// must not return until a cut, released once its writes are applied and
// the helper has had time to return, then a second one, moves them past
// it.
func TestLagBehindAnInstallReturnsOnlyPastTheStoppedNodesEnd(t *testing.T) {
	type tc struct {
		name                  string
		before, during, after int
	}
	runCase := func(t *testing.T, tcase tc) {
		c := newCluster(t, 3, 20)
		l := c.settledLeader(5 * time.Second)
		f := c.otherThan(l)
		var tok coreconsensus.TokenValue
		//: the writes f applies before it stops, its end far past 10.
		for i := range tcase.before {
			tok = proposeOnLeader(t, c, fmt.Sprintf("a%d=%d", i, i))
		}
		c.waitApplied(tok.Index, 10*time.Second)
		compactedPast(t, c, 10)
		var live []*testNode
		var releases []func()
		//: every node but f keeps its log where it is until the release.
		for _, tn := range c.nodes {
			//: f's own cuts are not held: it is the node stopped.
			if tn != f {
				live = append(live, tn)
				releases = append(releases, holdCuts(t, tn.node))
			}
		}
		target := tok.Index + uint64(tcase.during)
		var mu sync.Mutex
		returned := false
		stop, done := make(chan struct{}), make(chan struct{})
		t.Cleanup(func() {
			close(stop)
			<-done
		})
		go func() {
			defer close(done)
			//: the release waits until every live node applied the helper's writes.
			for _, tn := range live {
				//: polled, as nothing signals an applied index.
				for tn.node.Status().Applied < target {
					select {
					//: the test ended first.
					case <-stop:
						return
					//: polled again.
					case <-time.After(5 * time.Millisecond):
					}
				}
			}
			select {
			//: the test ended first.
			case <-stop:
				return
			// A helper that does not wait on the cuts returns within its
			// last write, long before this grace ends.
			case <-time.After(200 * time.Millisecond):
			}
			mu.Lock()
			held := returned
			//: a helper that returned is judged with the cuts still held.
			if !held {
				for _, release := range releases {
					release()
				}
			}
			mu.Unlock()
			//: the helper returned on its own: nothing more is written.
			if held {
				return
			}
			// A log compacts back to the older of its two newest snapshots:
			// the cut the release makes is one, the writes below make the
			// other, both past the stopped node's end.
			for i := range tcase.after {
				//: the test ended first.
				if !proposeAnywhere(live, fmt.Sprintf("b%d=%d", i, i), stop) {
					return
				}
			}
		}()
		lagBehindAnInstall(t, c, f, tcase.during)
		mu.Lock()
		returned = true
		_, end := f.log.Bounds()
		firsts := make([]uint64, len(live))
		for i, tn := range live {
			firsts[i], _ = tn.log.Bounds()
		}
		mu.Unlock()
		for i, tn := range live {
			//: a live log that starts at or below end+1 still knows the term at f's end.
			if firsts[i] <= end+1 {
				t.Errorf("lagBehindAnInstall returned with node %d's log starting at %d, at or below %d, the entry after the stopped node's end %d: it catches up by appends, not an install",
					tn.id, firsts[i], end+1, end)
			}
		}
	}
	for _, tcase := range []tc{
		{name: "the end of laggingWithOwnSnapshots", before: 60, during: 30, after: 30},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}

// proposeAnywhere proposes kv on whichever of nodes accepts it, off the
// test's goroutine, until one does or stop is closed; it reports whether
// one did.
func proposeAnywhere(nodes []*testNode, kv string, stop <-chan struct{}) bool {
	for {
		//: each node in turn: a follower forwards, the leader proposes.
		for _, tn := range nodes {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _, err := tn.node.Propose(ctx, []byte(kv))
			cancel()
			//: one accepted and committed it.
			if err == nil {
				return true
			}
		}
		select {
		//: the test ended first.
		case <-stop:
			return false
		//: tried again once an election may have settled.
		case <-time.After(10 * time.Millisecond):
		}
	}
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
// was isolated for good (#526). A refused Restore now stops the node
// (#1582): its copy stays for the next start, which the cause gone feeds.
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
	if !holdsNoTable(t, f) {
		t.Fatal("setup: the node started with a table")
	}
	c.net.Isolate(f.id, false)
	// An entry past the base the node holds: the append it is sent is
	// answered with a request for a snapshot.
	proposeOnLeader(t, c, "nudge=1")
	deadline := time.Now().Add(10 * time.Second)
	//: until a replacement's refused Restore stops the node.
	for refused.Load() < 1 || !f.node.stopped.Load() {
		//: not in time.
		if time.Now().After(deadline) {
			t.Fatalf("no replacement's refused Restore stopped the node (refused %d)", refused.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	//: the refusal stops the node STATE_MACHINE_FAILED.
	if reason, _ := f.node.StopReason(); !errs.HasCode(reason.Err, coreconsensus.CodeStateMachineFailed) {
		t.Errorf("a refused Restore stopped the node with %v, want STATE_MACHINE_FAILED", reason.Err)
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
	// The leader commits with two of the three nodes: f may not have heard
	// of it yet, and a node refuses NOT_LEADER an offer from another node
	// than the leader it knows.
	waitFor(t, "the follower knows the leader", func() bool {
		return f.node.Status().Leader == l.id
	})
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
	if holdsNoTable(t, tn) {
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
// snapshot bodies the node opens for the peer it watches, and the streams
// its peers refused as corrupt. A body is opened only once the peer
// accepted the offer: a stream refused before, on a route still cut or an
// offer turned down, opens none (#1577).
type streamingTransport struct {
	*MemoryTransport
	opened, corrupt atomic.Int64
	watch           atomic.Uint64
}

// Stream implements Transport, counting the bodies opened for the watched
// peer and the stream's verdict.
func (t *streamingTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	//: only a body streamed to the watched peer counts.
	if uint64(to) == t.watch.Load() {
		inner := open
		open = func(off int64) (io.ReadCloser, error) {
			rc, err := inner(off)
			//: a body that opened is one streamed to the peer.
			if err == nil {
				t.opened.Add(1)
			}
			return rc, err
		}
	}
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
	//: from the restart on, every node counts the bodies it opens for the lagging node.
	for _, st := range streaming {
		st.watch.Store(uint64(f.id))
	}
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
	// The catch-up was an install, not appends from a live log that still
	// covered the node's end (#1577): a snapshot body was opened for the
	// node after its restart. A stream counted as it began also counted
	// one refused on the route still cut, which streamed nothing.
	mu.Lock()
	var opened int64
	//: every node's bodies, whichever led while the node caught up.
	for _, st := range streaming {
		opened += st.opened.Load()
	}
	mu.Unlock()
	//: no node streamed a snapshot: the test ran without its subject.
	if opened == 0 {
		t.Fatal("the lagging node caught up with no snapshot body opened for it")
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

// openRefused is a snapshot store whose Open, from an install's restore
// once armed, is refused: the engine refuses the snapshot before any
// Restore, and the machine is never handed it.
type openRefused struct {
	coreconsensus.SnapshotStore
	armed   atomic.Bool
	refused atomic.Int64
}

// Open implements SnapshotStore, refusing a restore's open once armed.
func (s *openRefused) Open(id string) (coreconsensus.SnapshotHandle, error) {
	//: only a restore's open, once armed, is refused.
	if s.armed.Load() && calledFrom(".(*Node).restoreFrom") {
		s.refused.Add(1)
		return nil, origin(coreconsensus.SnapshotCorrupt, errs.String("why", "the test refuses the restore's open"))
	}
	return s.SnapshotStore.Open(id)
}

// A node with no table whose install the engine refuses before Restore —
// the snapshot unreadable, no machine touched — keeps running and keeps the
// copy its members are read from (#526): only a Restore the machine refused
// stops the node (#1582). Once the cause is gone, the next offer installs.
func TestANodeWithNoTableKeepsItsCopyThroughAnEngineRefusal(t *testing.T) {
	type tc struct{ name string }
	runCase := func(t *testing.T, _ tc) {
		var fID atomic.Uint64
		var store atomic.Pointer[openRefused]
		c := newClusterWith(t, &cluster{
			t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 7}, every: 20,
			configure: func(tn *testNode, cfg *Config) {
				//: only the node with no table opens through the double.
				if uint64(tn.id) != fID.Load() {
					return
				}
				s := &openRefused{SnapshotStore: tn.snaps}
				store.Store(s)
				cfg.Snapshots = s
			},
		})
		l := c.settledLeader(5 * time.Second)
		f := c.otherThan(l)
		tok := lagBehindAnInstall(t, c, f, 120)
		c.start(f)
		c.net.Isolate(f.id, false)
		c.waitApplied(tok.Index, 10*time.Second)
		// The installed copy rots; the node restarts with no table.
		c.net.Isolate(f.id, true)
		c.kill(f)
		held := holdOnlyTheLeadersNewest(t, l, f)
		fID.Store(uint64(f.id))
		c.start(f)
		//: the setup starts the node with no table.
		if !holdsNoTable(t, f) {
			t.Fatal("setup: the node started with a table")
		}
		s := store.Load()
		s.armed.Store(true)
		c.net.Isolate(f.id, false)
		proposeOnLeader(t, c, "nudge=1")
		deadline := time.Now().Add(10 * time.Second)
		//: until two installs were refused before Restore.
		for s.refused.Load() < 2 {
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatalf("no install reached the restore's open twice (refused %d)", s.refused.Load())
			}
			time.Sleep(5 * time.Millisecond)
		}
		//: an engine refusal never stops the node.
		if reason, stopped := f.node.StopReason(); stopped {
			t.Fatalf("an install the engine refused before Restore stopped the node: %v", reason.Err)
		}
		//: the node with no table keeps a copy to read its members from.
		if metas := must(f.snaps.List())(t); len(metas) == 0 {
			t.Errorf("the node with no table was left no snapshot (it held %s)", held)
		}
		// The cause is gone: the next offer installs.
		s.armed.Store(false)
		tok = proposeOnLeader(t, c, "after=1")
		c.waitApplied(tok.Index, 10*time.Second)
		//: the node is fed the whole state.
		if v, _ := f.sm.get("k5"); v != "5" {
			t.Fatalf("the node was never installed: k5=%q", v)
		}
	}
	//: one case, its own cluster.
	for _, tt := range []tc{{name: "an open refused at the restore keeps the copy and the node"}} {
		t.Run(tt.name, func(t *testing.T) { runCase(t, tt) })
	}
}
