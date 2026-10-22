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
