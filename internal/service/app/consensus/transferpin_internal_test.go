package consensus

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
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
}

func (t *holdTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
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
	err := t.MemoryTransport.Stream(ctx, to, offer, open)
	if err == nil {
		t.streams.Add(1)
		if f := t.streamed.Load(); f != nil {
			(*f)()
		}
	}
	return err
}

// #550: a transfer in flight pins compaction (ADR 0152 D7, R6-01). A
// leader that cut twice while a snapshot streamed compacted past the
// snapshot it was sending: once the follower installed it, the entry after
// it was gone, the leader offered its newest snapshot again, and under a
// sustained write rate the follower installed snapshot after snapshot and
// never caught up.
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
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never compacted")
		}
	}
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	c.start(lag)
	n := 0
	for round := range 4 {
		select {
		case <-lt.started:
		case <-time.After(10 * time.Second):
			f, _ := l.log.Bounds()
			t.Fatalf("round %d: no snapshot offered to the lagging node: lag applied %d leader first %d lag snaps %d streams %d lagfirst %v", round, lag.node.Status().Applied, f, len(must(lag.snaps.List())(t)), lt.streams.Load(), func() uint64 { a, _ := lag.log.Bounds(); return a }())
		}
		// Two cuts' worth of writes while the transfer is held.
		for i := range 45 {
			propose(t, l.node, fmt.Sprintf("r%d-%d=v", round, i))
			n++
		}
		if round > 0 {
			lt.holding.Store(false)
		}
		lt.release <- struct{}{}
		if round == 0 {
			// The offer that follows the first transfer, if any, is held
			// again: it is the defect.
			ok := false
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if v, _ := lag.sm.get("r0-44"); v == "v" {
					ok = true
					break
				}
			}
			if ok {
				lt.holding.Store(false)
				return
			}
			continue
		}
		t.Fatalf("the lagging node needed another snapshot after the first transfer: offers held %d, streams completed %d, its status %+v, the leader's log starts at %d",
			round+1, lt.streams.Load(), lag.node.Status(), func() uint64 { f, _ := l.log.Bounds(); return f }())
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
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never compacted")
		}
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
	c.start(lag)
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot offered to the lagging node")
	}
	lt.release <- struct{}{}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the snapshot stream never ended")
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
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never compacted")
		}
	}
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	// The node goes silent as its stream ends: its match stays where the
	// snapshot left it.
	cutOff := func() { c.net.Isolate(lag.id, true) }
	lt.streamed.Store(&cutOff)
	lt.holding.Store(true)
	c.start(lag)
	var offer coreconsensus.InstallOfferValue
	select {
	case offer = <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot offered to the lagging node")
	}
	lt.holding.Store(false)
	lt.release <- struct{}{}
	for i := range 45 {
		propose(t, l.node, fmt.Sprintf("c%d=v", i))
	}
	// No write from here on. The pin lasts ten maximum election timeouts,
	// two seconds here.
	held := offer.Meta.Position.Index
	for deadline := time.Now().Add(6 * time.Second); ; time.Sleep(10 * time.Millisecond) {
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
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never compacted")
		}
	}
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	c.start(lag)
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot offered to the lagging node")
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
