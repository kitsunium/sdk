package consensus

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// heartbeatFilter drops every heartbeat sent to the node the shared target
// names while it is not 0.
type heartbeatFilter struct {
	*MemoryTransport
	target *atomic.Uint64
}

// Send implements Transport, dropping the heartbeats the filter names.
func (f heartbeatFilter) Send(to coreconsensus.NodeID, frame []byte) {
	if t := f.target.Load(); t != 0 && coreconsensus.NodeID(t) == to {
		var m coreconsensus.MessageValue
		if wire.ParseMessage(bytes.Clone(frame), &m) == nil && m.Kind == coreconsensus.MsgHeartbeat {
			return
		}
	}
	f.MemoryTransport.Send(to, frame)
}

// gateFrom blocks every Apply at or past index from until gate closes.
type gateFrom struct {
	*kv
	from uint64
	gate chan struct{}
}

func (g gateFrom) Apply(e coreconsensus.EntryValue) any {
	if e.Position.Index >= g.from {
		<-g.gate
	}
	return g.kv.Apply(e)
}

// TestARestartedFollowersReadIsNeverAnsweredForItsPredecessor is #510: a
// leader still holds a read of a follower's previous incarnation, its round
// unconfirmed, when the follower restarts, a write W commits and is
// acknowledged, and the new incarnation calls Barrier. Once the heartbeats
// flow again the leader answers both reads in arrival order; the old one
// carries the commit index from before W. Under read contexts restarting at
// 1, the new Barrier took that answer and returned below W, which its state
// machine had not applied: a stale read.
func TestARestartedFollowersReadIsNeverAnsweredForItsPredecessor(t *testing.T) {
	var drop atomic.Uint64
	var gated atomic.Pointer[gateFrom]
	var follower atomic.Uint64
	c := &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 1, 0}, size: 3,
		timing: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 20 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.05,
		},
	}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.Transport = heartbeatFilter{MemoryTransport: NewMemoryTransport(c.net, tn.id), target: &drop}
		if g := gated.Load(); g != nil && uint64(tn.id) == follower.Load() {
			g.kv = tn.sm
			cfg.Machine = *g
		}
	}
	newClusterWith(t, c)
	l := c.settledLeader(10 * time.Second)
	f, g := c.otherThan(l), (*testNode)(nil)
	for _, tn := range c.nodes {
		if tn != l && tn != f {
			g = tn
		}
	}
	c.net.Isolate(g.id, true)

	// The old incarnation's read: its heartbeats dropped, the leader's
	// round is never confirmed, and the caller gives up.
	drop.Store(uint64(f.id))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err := f.node.Barrier(ctx)
	cancel()
	if err == nil {
		t.Fatal("setup: the old incarnation's Barrier returned with its heartbeats dropped")
	}
	before := l.node.Status().Commit

	// The restart, its state machine gated from the next index on.
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	follower.Store(uint64(f.id))
	gated.Store(&gateFrom{from: before + 1, gate: gate})
	c.kill(f)
	c.start(f)
	w := propose(t, l.node, "w=1")
	if w.Index <= before {
		t.Fatalf("setup: W at %d, not past %d", w.Index, before)
	}

	type result struct {
		tok coreconsensus.TokenValue
		err error
	}
	got := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tok, err := f.node.Barrier(ctx)
		got <- result{tok, err}
	}()
	time.Sleep(50 * time.Millisecond)
	drop.Store(0)
	select {
	case r := <-got:
		if r.err == nil && r.tok.Index < w.Index {
			t.Fatalf("STALE READ: the restarted follower's Barrier returned at %d, below W at %d acknowledged before the call (leader commit before W %d)",
				r.tok.Index, w.Index, before)
		}
		t.Fatalf("the Barrier returned %v, %v before its state machine applied W", r.tok, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	close(gate)
	r := <-got
	if r.err != nil || r.tok.Index < w.Index {
		t.Fatalf("the Barrier answered %v, %v; want a token at or past W at %d", r.tok, r.err, w.Index)
	}
}
