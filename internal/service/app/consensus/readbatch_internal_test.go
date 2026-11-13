package consensus

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// manualStep is how far the manual clock moves at a time: one tick of the
// 100 ms heartbeat.
const manualStep = 20 * time.Millisecond

// heartbeatCounter counts the heartbeats a node sends, per peer.
type heartbeatCounter struct {
	*MemoryTransport
	mu   *sync.Mutex
	sent map[[2]coreconsensus.NodeID]int
	from coreconsensus.NodeID
}

// Send implements Transport, counting every heartbeat on the way.
func (h heartbeatCounter) Send(to coreconsensus.NodeID, frame []byte) {
	var m coreconsensus.MessageValue
	if wire.ParseMessage(bytes.Clone(frame), &m) == nil && m.Kind == coreconsensus.MsgHeartbeat {
		h.mu.Lock()
		h.sent[[2]coreconsensus.NodeID{h.from, to}]++
		h.mu.Unlock()
	}
	h.MemoryTransport.Send(to, frame)
}

// manualCluster is a three-node memory cluster on one ManualClock, whose
// heartbeats are counted, with reads served in mode reads.
type manualCluster struct {
	*cluster
	clk  *clock.ManualClock
	mu   sync.Mutex
	sent map[[2]coreconsensus.NodeID]int
}

func newManualCluster(t *testing.T, reads ReadMode) *manualCluster {
	t.Helper()
	mc := &manualCluster{clk: clock.NewManualClock(time.Unix(1_000_000, 0).UTC()), sent: map[[2]coreconsensus.NodeID]int{}}
	// A drift budget of half the 1 s window: a turn the test's Advance
	// lands in reads as long as the step, never as long as the budget, so
	// no turn drops the lease.
	c := &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{11, 0, 8}, size: 3,
		timing: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.5,
		},
	}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.Clock = mc.clk
		cfg.Reads = reads
		cfg.Transport = heartbeatCounter{MemoryTransport: NewMemoryTransport(c.net, tn.id), mu: &mc.mu, sent: mc.sent, from: tn.id}
	}
	mc.cluster = newClusterWith(t, c)
	return mc
}

// elect advances the clock a tick at a time until a leader has confirmed a
// read of its own term, then stops it: from there on no tick fires, and
// every heartbeat is one a read opened.
func (mc *manualCluster) elect(t *testing.T) *testNode {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for range 10 {
			mc.clk.Advance(manualStep)
			time.Sleep(200 * time.Microsecond)
		}
		for _, tn := range mc.nodes {
			if tn.node.Status().Role != coreconsensus.RoleLeader {
				continue
			}
			done := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, err := tn.node.Barrier(ctx)
				done <- err
			}()
			if mc.settle(done) == nil {
				return tn
			}
		}
	}
	t.Fatal("setup: no leader confirmed a read on the manual clock")
	return nil
}

// settle keeps the clock moving until the read that answers on done ends,
// and returns its error.
func (mc *manualCluster) settle(done <-chan error) error {
	for {
		select {
		case err := <-done:
			return err
		case <-time.After(time.Millisecond):
			mc.clk.Advance(manualStep)
		}
	}
}

// heartbeats is how many heartbeats from sent to each of its peers.
func (mc *manualCluster) heartbeats(from coreconsensus.NodeID) map[coreconsensus.NodeID]int {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	out := map[coreconsensus.NodeID]int{}
	for k, v := range mc.sent {
		if k[0] == from {
			out[k[1]] = v
		}
	}
	return out
}

// readRounds is the leader's count of read rounds, read on its loop.
func readRounds(l *testNode) uint64 {
	var n uint64
	l.node.exec(func() { n = l.node.r.ReadRounds() })
	return n
}

// burstOfBarriers holds l's loop, queues reads Barriers behind it, then lets
// the loop go and waits for every one of them to return.
func burstOfBarriers(t *testing.T, l *testNode, reads int) {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	go l.node.exec(func() {
		close(held)
		<-release
	})
	<-held
	errc := make(chan error, reads)
	for range reads {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := l.node.Barrier(ctx)
			errc <- err
		}()
	}
	waitFor(t, "every Barrier queued behind the held loop", func() bool {
		l.node.readq.mu.Lock()
		defer l.node.readq.mu.Unlock()
		return len(l.node.readq.items) == reads
	})
	close(release)
	for range reads {
		if err := <-errc; err != nil {
			t.Fatalf("a Barrier of the burst failed: %v", err)
		}
	}
}

// TestBarriersQueuedInOneTurnShareOneRound is #1108: every Barrier handed
// the loop its own closure, each closure's Ready flushed its read alone, and
// N concurrent reads cost N heartbeat broadcasts. 256 Barriers queued
// behind a held loop on an idle leader, its clock stopped so no tick
// heartbeats, now share one round: one heartbeat per peer.
func TestBarriersQueuedInOneTurnShareOneRound(t *testing.T) {
	mc := newManualCluster(t, ReadIndex)
	l := mc.elect(t)
	before, rounds := mc.heartbeats(l.id), readRounds(l)
	burstOfBarriers(t, l, 256)
	after := mc.heartbeats(l.id)
	for _, tn := range mc.nodes {
		if tn == l {
			continue
		}
		if d := after[tn.id] - before[tn.id]; d >= 16 || d < 1 {
			t.Errorf("peer %d got %d heartbeats for 256 queued reads, want 1 to 15", tn.id, d)
		}
	}
	// A follower's leader probe sent at the last tick may open a round of
	// its own; one Barrier a round would open 256.
	if got := readRounds(l) - rounds; got < 1 || got >= 16 {
		t.Errorf("256 reads queued in one turn opened %d rounds, want 1 to 15", got)
	}
}

// TestLeasedBarriersOpenNoRound is #1108's lease side: under ReadLease, with
// the lease valid on a stopped clock, the same burst is answered at once
// and sends no heartbeat: none of a round, none of a tick.
func TestLeasedBarriersOpenNoRound(t *testing.T) {
	mc := newManualCluster(t, ReadLease)
	l := mc.elect(t)
	// A read on the stopped clock: its round, if the lease lapsed while the
	// election ran, extends the lease from now.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := l.node.Barrier(ctx)
	cancel()
	if err != nil {
		t.Fatalf("setup: a read on the stopped clock: %v", err)
	}
	valid := false
	l.node.exec(func() { valid = l.node.r.LeaseValid(l.node.clk.Now()) })
	if !valid {
		t.Fatal("setup: the leader holds no lease after a confirmed read")
	}
	before, rounds := mc.heartbeats(l.id), readRounds(l)
	burstOfBarriers(t, l, 256)
	after := mc.heartbeats(l.id)
	for _, tn := range mc.nodes {
		if tn == l {
			continue
		}
		if d := after[tn.id] - before[tn.id]; d != 0 {
			t.Errorf("peer %d got %d heartbeats for 256 leased reads, want 0", tn.id, d)
		}
	}
	if got := readRounds(l) - rounds; got != 0 {
		t.Errorf("256 leased reads opened %d rounds, want 0", got)
	}
}

// TestABarrierQueuedAfterTheLoopEndedIsAnswered: a read pushed once the loop
// has run its last turn is answered by no one; Barrier returns NODE_STOPPED
// instead of waiting for its context.
func TestABarrierQueuedAfterTheLoopEndedIsAnswered(t *testing.T) {
	c := newCluster(t, 1, 0)
	n := c.nodes[0].node
	if err := n.HardClose(); err != nil {
		t.Fatal(err)
	}
	w := &readWait{done: make(chan readResult, 1)}
	n.readq.push(w)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok := n.awaitRead(ctx, w); ok || ctx.Err() != nil {
		t.Fatalf("a read queued after the loop ended: answered %v, context %v", ok, ctx.Err())
	}
}
