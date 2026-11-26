package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// holdTransport is a memory transport whose snapshot streams wait, while
// holding is on, until the test releases them one by one.
type holdTransport struct {
	*MemoryTransport
	holding atomic.Bool
	started chan coreconsensus.InstallOfferValue
	release chan struct{}
	streams atomic.Int32
	// cancelled counts the held streams whose context ended first.
	cancelled atomic.Int32
	// streamed, when set, runs after a stream that succeeded, before
	// Stream returns.
	streamed atomic.Pointer[func()]
	// answered, when set, runs once a stream to a peer succeeded, before it
	// is counted: the receiver installed it and answered, and the leader has
	// not heard that answer yet (#1570).
	answered atomic.Pointer[func(coreconsensus.NodeID)]
	// drained, when set, runs once a stream's body was read to its end:
	// before the receiver commits the install and answers it, which
	// streamed comes after.
	drained atomic.Pointer[func()]
	// inFlight counts the Stream calls not returned yet, and peak the
	// most at once: what MaxSnapshotStreams bounds (#920).
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (t *holdTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	for n := t.inFlight.Add(1); ; {
		p := t.peak.Load()
		if n <= p || t.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer t.inFlight.Add(-1)
	if t.holding.Load() {
		select {
		case t.started <- offer:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-t.release:
		case <-ctx.Done():
			t.cancelled.Add(1)
			return ctx.Err()
		}
	}
	if f := t.drained.Load(); f != nil {
		open = onEOF(open, *f)
	}
	err := t.MemoryTransport.Stream(ctx, to, offer, open)
	if err == nil {
		//: A hook set holds the count until it returns.
		if f := t.answered.Load(); f != nil {
			(*f)(to)
		}
		t.streams.Add(1)
		if f := t.streamed.Load(); f != nil {
			(*f)()
		}
	}
	return err
}

// onEOF is open whose body runs hook once a read of it returns io.EOF.
func onEOF(open func(int64) (io.ReadCloser, error), hook func()) func(int64) (io.ReadCloser, error) {
	return func(off int64) (io.ReadCloser, error) {
		rc, err := open(off)
		if err != nil {
			return rc, err
		}
		return &eofReader{ReadCloser: rc, hook: hook}, nil
	}
}

// eofReader runs hook, once, when its body returns io.EOF.
type eofReader struct {
	io.ReadCloser
	hook func()
	once sync.Once
}

// Read implements io.Reader.
func (r *eofReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		r.once.Do(r.hook)
	}
	return n, err
}

// compactedPastItsEnd waits until the leader's log starts past the end of
// the stopped node's, so that node, restarted, is offered a snapshot. It
// used to wait for the log to start past index 100, which the leader may
// not reach with its writes done: a cut still in flight when more fell due
// is made once, at the index applied when it ends, and the two snapshots
// kept — the older one, less SnapshotTrailing, is where the log starts —
// can then lie far apart, the older below 100, with nothing left to cut. A
// slow runner (macOS, Solaris) cut that way, and the setup failed (#588).
// The leader's cuts are let settle too, so the snapshot it offers is the
// one at the end of its writes, whatever the pace of its cuts.
//
// The node hears nothing from here until restartHearingOnly: it is cut off
// from every member, and the frames the memory network still queues for it
// are dropped. Its end is then the end it restarts with. The network kept
// queueing what the leader sent it while it was down, the appends of the
// window it was replicating in, and delivered them at its restart: the node
// came back holding the entries its end, read here, did not count. With
// the leader's log starting at 17, its cuts stalled as above, the appends
// queued reached past it, the node caught up by appends, and no snapshot
// was offered (#1547).
func compactedPastItsEnd(t *testing.T, c *cluster, l, lag *testNode) {
	t.Helper()
	stopHearing(c, lag)
	_, end := lag.log.Bounds()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > end+1 {
			cutsSettled(t, l)
			return
		}
		if time.Now().After(deadline) {
			first, _ := l.log.Bounds()
			t.Fatalf("setup: the leader's log still starts at %d, not past the stopped node's end %d", first, end)
		}
	}
}

// stopHearing cuts stopped node tn off from every member, both ways, then
// drops the frames its end of the memory network still queues: delivered
// at its restart, they were entries it did not hold when it stopped
// (#1547). Cut first, nothing is queued for it after the drop.
func stopHearing(c *cluster, tn *testNode) {
	c.net.Isolate(tn.id, true)
	tr := NewMemoryTransport(c.net, tn.id)
	for {
		select {
		case <-tr.ctrl:
		case <-tr.data:
		default:
			return
		}
	}
}

// restartHearingOnly restarts tn, which compactedPastItsEnd stopped hearing,
// then links it, both ways, to the members of hear and to no other. It
// starts cut off, so nothing reaches it before it runs but what a live node
// would be sent.
//
// A test whose leader holds a stream names that leader alone. Restarted,
// the node knows no leader and grants any PreVote: a follower whose
// election timer fired under a loaded -race run then won an election with
// it, and the leader whose stream the test holds lost its leadership
// before it offered one — one run in a few hundred (#588). Cut off, that
// follower's PreVote reaches only the leader, which refuses it.
//
// The leader itself must not step down either: with testTimingFast's
// CheckQuorum window of 100 ms, a loop the loaded runner did not schedule
// for 130 ms heard from no peer and stepped down (traced under -race
// -cpu=4). These tests run on steadyTiming, whose window is 500 ms.
func restartHearingOnly(c *cluster, tn *testNode, hear ...*testNode) {
	c.start(tn)
	for _, other := range c.nodes {
		if other == tn {
			continue
		}
		cut := !slices.Contains(hear, other)
		c.net.Cut(tn.id, other.id, cut)
		c.net.Cut(other.id, tn.id, cut)
	}
}

// streamState is what leader l holds for peer to while a test waits for
// its offer, read on l's loop: raft's view of the peer, none off the
// leader, and the snapshot streams in flight and waiting.
func streamState(l *testNode, to coreconsensus.NodeID) string {
	s := "the leader's loop stopped"
	l.node.exec(func() {
		var peer any = "not tracked"
		for _, p := range l.node.r.PeerStats() {
			if p.ID == to {
				peer = p
			}
		}
		_, inFlight := l.node.sendStop[to]
		s = fmt.Sprintf("the leader's status %+v, its view of the peer %+v, the peer's stream in flight %v, streams in flight %d, waiting %+v",
			l.node.Status(), peer, inFlight, len(l.node.sendStop), l.node.streamWait)
	})
	return s
}

// releaseHeld lets the stream the leader holds go on. The stream waits for
// its release only while its context lives: a leader that lost its
// leadership stops its streams (#547), and an unbounded send then blocked
// the test for good — an iteration in about 270 under load hung there
// until the binary's timeout (#598). The send is bounded, and its failure
// says why nothing took it.
func releaseHeld(t *testing.T, lt *holdTransport, l *testNode) {
	t.Helper()
	select {
	case lt.release <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatalf("the held snapshot stream no longer waits for its release: %d held streams cancelled, the leader's status %+v", lt.cancelled.Load(), l.node.Status())
	}
}

// steadyTiming is testTimingFast with election timeouts of 500 ms to 1 s,
// for a test whose leader must keep its leadership throughout.
func steadyTiming() coreconsensus.TimingContractSpec {
	tc := testTimingFast()
	tc.ElectionTimeoutMin, tc.ElectionTimeoutMax = 500*time.Millisecond, time.Second
	return tc
}

// #550: a transfer in flight pins compaction (ADR 0152 D7, R6-01). A
// leader that cut twice while a snapshot streamed compacted past the
// snapshot it was sending: once the follower installed it, the entry after
// it was gone, the leader offered its newest snapshot again, and under a
// sustained write rate the follower installed snapshot after snapshot and
// never caught up.
//
// The leader must keep its leadership throughout: one that lost it stopped
// the stream the test held (#547), and the release the test sent then had
// no taker (#598). The case runs on steadyTiming and hears the restarted
// node only from the leader, as its siblings do since #588.
func TestATransferInFlightPinsCompaction(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		ht := &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		if old := trs[tn.id]; old != nil {
			ht.started, ht.release = old.started, old.release
			ht.holding.Store(old.holding.Load())
		}
		trs[tn.id] = ht
		cfg.Transport = ht
		cfg.InitialTiming = steadyTiming()
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, c, l, lag)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	restartHearingOnly(c, lag, l)
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		f, _ := l.log.Bounds()
		t.Fatalf("no snapshot offered to the lagging node: lag applied %d leader first %d lag snaps %d streams %d lagfirst %v", lag.node.Status().Applied, f, len(must(lag.snaps.List())(t)), lt.streams.Load(), func() uint64 { a, _ := lag.log.Bounds(); return a }())
	}
	// Two cuts' worth of writes while the transfer is held.
	for i := range 45 {
		propose(t, l.node, fmt.Sprintf("r0-%d=v", i))
	}
	// The offer that follows the first transfer, if any, is held again: it
	// is the defect. The node catches up by appends, or is offered that
	// second snapshot, whichever comes first.
	releaseHeld(t, lt, l)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if v, _ := lag.sm.get("r0-44"); v == "v" {
			lt.holding.Store(false)
			return
		}
		select {
		case <-lt.started:
			t.Fatalf("the lagging node needed another snapshot after the first transfer: streams completed %d, its status %+v, the leader's log starts at %d",
				lt.streams.Load(), lag.node.Status(), func() uint64 { f, _ := l.log.Bounds(); return f }())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lagging node never caught up after the first transfer: streams completed %d, its status %+v", lt.streams.Load(), lag.node.Status())
		}
	}
}

// #550, the window after the stream: once a snapshot streamed, the leader
// only probes the node, and sends it entries after the node answers. A cut
// in that window compacted past the entry after the snapshot, and the node
// was offered another snapshot. The node is cut off from the leader as its
// stream ends, two cuts' worth of entries are written, then the link heals:
// the node must catch up by appends.
func TestAStreamedSnapshotPinsCompactionUntilThePeerCatchesUp(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 4}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		trs[tn.id] = &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		cfg.Transport = trs[tn.id]
		// A pin outlives the stream by ten maximum election timeouts: ten
		// seconds here, well past what the cut-off below lasts.
		cfg.InitialTiming = testTimingFast()
		cfg.InitialTiming.ElectionTimeoutMin, cfg.InitialTiming.ElectionTimeoutMax = 500*time.Millisecond, time.Second
	}
	newClusterWith(t, c)
	l := c.settledLeader(10 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, c, l, lag)
	// A cut on request, at the last index: the snapshot the node is
	// offered next.
	if _, err := l.node.SaveSnapshot(context.Background(), "op:test", io.Discard); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	link := func(cut bool) {
		c.net.Cut(l.id, lag.id, cut)
		c.net.Cut(lag.id, l.id, cut)
	}
	ended := make(chan struct{})
	var once sync.Once
	cutOff := func() { once.Do(func() { link(true); close(ended) }) }
	lt.streamed.Store(&cutOff)
	lt.holding.Store(true)
	restartHearingOnly(c, lag, c.nodes...)
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot offered to the lagging node")
	}
	// Silent from the install on: an answer it sent the leader between the
	// install and the cut-off would have caught it up, and its pin ended
	// with every check below passing for that reason alone.
	c.net.Cut(lag.id, l.id, true)
	releaseHeld(t, lt, l)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the snapshot stream never ended")
	}
	// The snapshot streamed is at the leader's last index: its cuts settled
	// before the offer, with nothing written since. Its pin used to count
	// as caught up at once — the pin's index, not the node's match, reached
	// the last index — and the first tick ended it: the cuts of the writes
	// below compacted past the entry after it, and the node was offered a
	// second snapshot (#588).
	var pin sendPin
	waitFor(t, "the streamed snapshot's pin", func() bool {
		l.node.exec(func() { pin = l.node.sending[lag.id] })
		return !pin.until.IsZero()
	})
	var last uint64
	pinned := false
	l.node.exec(func() {
		last = l.node.r.Status().Last
		l.node.watchPins(l.node.clk.Now())
		_, pinned = l.node.sending[lag.id]
	})
	if pin.index != last {
		t.Fatalf("setup: the snapshot streamed is at %d, not at the leader's last index %d", pin.index, last)
	}
	if !pinned {
		t.Fatal("the pin of a snapshot at the leader's last index ended before the node answered")
	}
	var tok coreconsensus.TokenValue
	for i := range 45 {
		tok = propose(t, l.node, fmt.Sprintf("c%d=v", i))
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if metas := must(l.snaps.List())(t); len(metas) > 0 && metas[0].Position.Index+20 > tok.Index {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never cut after the stream")
		}
	}
	link(false)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		select {
		case <-lt.started:
			f, _ := l.log.Bounds()
			t.Fatalf("the node was offered a second snapshot: its status %+v, the leader's log starts at %d", lag.node.Status(), f)
		default:
		}
		if v, _ := lag.sm.get("c44"); v == "v" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node never caught up: %+v", lag.node.Status())
		}
	}
}

// A streamed snapshot's pin ends on its own bound when the peer never
// answers again, and the log is compacted then, with no write to cut a new
// snapshot: compaction only ran after a cut, so a pin that ended once the
// writes stopped held the log back until the next write.
func TestAPinThatEndsCompactsTheLogWithoutAWrite(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 5}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		trs[tn.id] = &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		cfg.Transport = trs[tn.id]
		cfg.InitialTiming = steadyTiming()
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, c, l, lag)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	// The node goes silent as its stream ends: its match stays where the
	// snapshot left it.
	cutOff := func() { c.net.Isolate(lag.id, true) }
	lt.streamed.Store(&cutOff)
	lt.holding.Store(true)
	restartHearingOnly(c, lag, l)
	var offer coreconsensus.InstallOfferValue
	select {
	case offer = <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot offered to the lagging node")
	}
	lt.holding.Store(false)
	releaseHeld(t, lt, l)
	for i := range 45 {
		propose(t, l.node, fmt.Sprintf("c%d=v", i))
	}
	// No write from here on. The pin lasts ten maximum election timeouts,
	// ten seconds here.
	held := offer.Meta.Position.Index
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > held {
			return
		}
		if time.Now().After(deadline) {
			f, _ := l.log.Bounds()
			t.Fatalf("the leader's log still starts at %d, not past the %d it streamed", f, held)
		}
	}
}

// #547, the sender's half: a leader that loses its leadership stops the
// snapshot stream it had in flight. Nothing cancelled it but a stop, so the
// stale stream ran on next to the new leader's install of the same
// snapshot, and its refusal at the end unlinked the receiver's shared
// spool: the fresh install failed STORAGE_FAILED on a healthy disk.
func TestALeaderThatLosesItsLeadershipStopsItsSnapshotStream(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		trs[tn.id] = &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue, 8), release: make(chan struct{}),
		}
		cfg.Transport = trs[tn.id]
		cfg.InitialTiming = steadyTiming()
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, c, l, lag)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	first, _ := l.log.Bounds()
	_, end := lag.log.Bounds()
	restartHearingOnly(c, lag, l)
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatalf("no snapshot offered to the lagging node, whose log ended at %d when the leader's started at %d: %s, the transport's streams in flight %d",
			end, first, streamState(l, lag.id), lt.inFlight.Load())
	}
	var to *testNode
	for _, tn := range c.nodes {
		if tn != l && tn != lag {
			to = tn
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.node.TransferLeadership(ctx, "op:test", to.id); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); lt.cancelled.Load() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the former leader's snapshot stream is still running after it lost its leadership")
		}
	}
	lt.holding.Store(false)
}

// slowRestoreKV is kv whose Restore takes delay, and counts its restores.
type slowRestoreKV struct {
	*kv
	delay    time.Duration
	restores *atomic.Int32
}

func (s slowRestoreKV) Restore(r io.Reader, m coreconsensus.SnapshotMetaValue) error {
	s.restores.Add(1)
	time.Sleep(s.delay)
	return s.kv.Restore(r, m)
}

// The #602 bound ended a stream's pin pinAfterStream maximum election
// timeouts after its last byte, whether the receiver was stuck or only slow
// to restore. A follower whose Restore took longer than that, under a
// steady write rate, was compacted past while it restored, and was sent
// snapshot after snapshot without ever catching up (#610, the #550 defect
// again). A receiver still answering when its pin falls due is given a
// bound twice as long at its next transfer, and catches up; a stuck one
// still does not hold compaction for good (TestAFollowerStuckInItsInstallDoesNotPinCompaction).
func TestAFollowerSlowerToRestoreThanThePinBoundCatchesUp(t *testing.T) {
	restores := new(atomic.Int32)
	var fid coreconsensus.NodeID
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if fid != 0 && tn.id == fid {
			// 3 s, past pinAfterStream x ElectionTimeoutMax = 2 s.
			cfg.Machine = slowRestoreKV{kv: tn.sm, delay: 3 * time.Second, restores: restores}
		}
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.kill(f)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	compactedPastItsEnd(t, c, l, f)
	fid = f.id
	restartHearingOnly(c, f, c.nodes...)
	// Steady writes, about a hundred a second, for at most 20 s.
	caught := false
	i := 0
	for stop := time.Now().Add(20 * time.Second); time.Now().Before(stop) && !caught; i++ {
		propose(t, l.node, fmt.Sprintf("b%05d=v", i))
		time.Sleep(10 * time.Millisecond)
		// Restarted behind the leader's compacted log, it catches up by a
		// snapshot, or by the log of a new leader that still holds it.
		caught = f.node.Status().Applied+5 >= l.node.Status().Commit
	}
	t.Logf("restores on the follower %d, caught up during the writes: %v, follower applied %d, leader commit %d",
		restores.Load(), caught, f.node.Status().Applied, l.node.Status().Commit)
	if !caught {
		t.Errorf("a follower whose Restore takes 3 s, past the 2 s pin bound, never caught up under writes: %d restores in 20 s", restores.Load())
	}
}

// #1547: the node compactedPastItsEnd waits behind restarts with the log
// whose end it read. Stopped while its leader replicated to it, the node
// was still sent the appends of its window, and the memory network queued
// them for it, to deliver at its restart: it came back past that end, and
// when the leader's log started near it, as it does when the leader's cuts
// stall, it caught up by appends, offered no snapshot, and the tests that
// wait for one failed one run in about two thousand. Nothing waits for it
// once the setup read its end.
func TestANodeStoppedBehindHearsNothingSentWhileItWasDown(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 6}, every: 20, size: 3}
	c.configure = func(_ *testNode, cfg *Config) { cfg.InitialTiming = steadyTiming() }
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	// Replicating to it, the leader sends it every new entry at once.
	waitFor(t, "the leader replicating its whole log to the node", func() bool {
		replicating := false
		l.node.exec(func() {
			last := l.node.r.Status().Last
			for _, p := range l.node.r.PeerStats() {
				if p.ID == lag.id {
					replicating = !p.Flags.Has(raft.StatProbing) && !p.Flags.Has(raft.StatSnapshot) && p.Match == last
				}
			}
		})
		return replicating
	})
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, c, l, lag)
	tr := NewMemoryTransport(c.net, lag.id)
	if data, ctrl := len(tr.data), len(tr.ctrl); data+ctrl != 0 {
		t.Fatalf("%d frames on the data lane and %d on the control lane wait for the stopped node: delivered at its restart, the appends among them extend the log whose end the setup read", data, ctrl)
	}
}
