package consensus

import (
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// gaugeOf is the dimensionless reading of name in m, and whether it was
// published.
func gaugeOf(m coremetrics.FullMeter, name string) (float64, bool) {
	g, ok := m.Collect().Gauges[name]
	//: an unpublished gauge, or one with no point yet.
	if !ok || len(g.Points) == 0 {
		return 0, false
	}
	return g.Points[0].Value, true
}

// peerLag is the leader's PeerHealthValue.Lag of peer id, and whether the
// leader reports the peer.
func peerLag(h *HealthValue, id coreconsensus.NodeID) (uint64, bool) {
	//: the leader's view of every peer.
	for _, p := range h.Peers {
		//: the peer asked about.
		if p.ID == id {
			return p.Lag, true
		}
	}
	return 0, false
}

// #919: a follower whose disk is held reports its replication lag growing
// with the writes it cannot make durable, as the leader's Lag of it does,
// and shrinking back to 0 once its disk is released; LeaderLastAppend says
// when it last took entries. consensus_replication_lag_entries follows on
// the follower and reads 0 on the leader.
func TestAFollowerOnASlowDiskReportsItsReplicationLag(t *testing.T) {
	var mu sync.Mutex
	meters := map[coreconsensus.NodeID]coremetrics.FullMeter{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 1, 9}, size: 3, timing: steadyTiming()}
	logOf := hangLogs(c)
	withLog := c.configure
	c.configure = func(tn *testNode, cfg *Config) {
		withLog(tn, cfg)
		m := svcmetrics.NewMeter()
		mu.Lock()
		meters[tn.id] = m
		mu.Unlock()
		cfg.Meter = m
	}
	newClusterWith(t, c)
	l := c.settledLeader(10 * time.Second)
	slow := c.otherThan(l)
	mu.Lock()
	lm, sm := meters[l.id], meters[slow.id]
	mu.Unlock()
	if h := l.node.Health(); h.ReplicationLagEntries != 0 || h.LeaderLastAppend >= 0 {
		t.Fatalf("the leader reports a replication lag %d and a last append %v, want 0 and negative",
			h.ReplicationLagEntries, h.LeaderLastAppend)
	}

	release := logOf(slow).hold()
	defer release()
	const writes = 40
	//: writes the slow follower takes but cannot make durable.
	for i := range writes {
		propose(t, l.node, fmt.Sprintf("lag%d=v", i))
	}
	var fLag, lLag uint64
	waitFor(t, "the slow follower's lag to grow and agree with the leader's", func() bool {
		fh, lh := slow.node.Health(), l.node.Health()
		fLag = fh.ReplicationLagEntries
		lLag, _ = peerLag(&lh, slow.id)
		// One batch: an acknowledgement in flight when the disk was held
		// may still move the leader's match.
		return fLag >= writes && max(fLag, lLag)-min(fLag, lLag) <= 1 && fh.LeaderLastAppend >= 0
	})
	waitFor(t, "consensus_replication_lag_entries on the slow follower", func() bool {
		v, ok := gaugeOf(sm, mReplicationLag)
		return ok && v >= writes
	})
	if v, ok := gaugeOf(lm, mReplicationLag); !ok || v != 0 {
		t.Fatalf("the leader publishes consensus_replication_lag_entries %v (published %v), want 0", v, ok)
	}

	release()
	waitFor(t, "the released follower's lag to shrink to 0 with the leader's", func() bool {
		fh, lh := slow.node.Health(), l.node.Health()
		lag, ok := peerLag(&lh, slow.id)
		return fh.ReplicationLagEntries == 0 && ok && lag == 0
	})
	waitFor(t, "consensus_replication_lag_entries back to 0 on the follower", func() bool {
		v, ok := gaugeOf(sm, mReplicationLag)
		return ok && v == 0
	})
	if h := slow.node.Health(); !h.Flags.Has(HealthReady) {
		t.Fatalf("the caught-up follower is not ready: %+v", h.Flags)
	}
}
