package consensus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// membershipCounter counts the admission sets its node hands it.
type membershipCounter struct {
	coreconsensus.Transport
	sets atomic.Int64
}

func (c *membershipCounter) SetMembership(g coreconsensus.GroupID, m []coreconsensus.MemberValue) {
	c.sets.Add(1)
	c.Transport.SetMembership(g, m)
}

// A membership is handed to the transport when it changes, never at every
// commit: each commit cost every node a SetMembership, a resolution of
// every member's address and an Info line (#531).
func TestAnUnchangedMembershipIsNotRepublished(t *testing.T) {
	var mu sync.Mutex
	trs := map[coreconsensus.NodeID]*membershipCounter{}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 3, 1},
		configure: func(tn *testNode, cfg *Config) {
			ct := &membershipCounter{Transport: cfg.Transport}
			mu.Lock()
			trs[tn.id] = ct
			mu.Unlock()
			cfg.Transport = ct
		},
	})
	l := c.settledLeader(5 * time.Second)
	tok := proposeOnLeader(t, c, "warm=1")
	c.waitApplied(tok.Index, 5*time.Second)
	counts := func() map[coreconsensus.NodeID]int64 {
		mu.Lock()
		defer mu.Unlock()
		out := map[coreconsensus.NodeID]int64{}
		for _, tn := range c.nodes {
			out[tn.id] = trs[tn.id].sets.Load()
		}
		return out
	}
	before := counts()
	for i := range 100 {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	after := counts()
	for id, n := range after {
		if n != before[id] {
			t.Errorf("node %d was handed the unchanged membership %d times over 100 commits", id, n-before[id])
		}
	}
	// A real change is handed on.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	learner := coreconsensus.MemberValue{ID: 0x5eed, Slot: "slot-learner", SPKI: MemorySPKI(0x5eed)}
	if err := l.node.AddLearner(ctx, "op:test", learner); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the change handed to every transport", func() bool {
		for id, n := range counts() {
			if n == after[id] {
				return false
			}
		}
		return true
	})
}

// gaugeAt is the value of name's series for peer id in meter's collection.
func gaugeAt(meter coremetrics.FullMeter, name string, id coreconsensus.NodeID) (float64, bool) {
	for _, p := range meter.Collect().Gauges[name].Points {
		for _, a := range p.Attrs {
			if a.Key == "peer" && a.Str() == id.String() {
				return p.Value, true
			}
		}
	}
	return 0, false
}

// meteredCluster is a three-node cluster, one meter per node, whose
// transports report the certificate expiry certs holds for each peer.
func meteredCluster(t *testing.T) (*cluster, map[coreconsensus.NodeID]coremetrics.FullMeter, *certTable) {
	meters := map[coreconsensus.NodeID]coremetrics.FullMeter{}
	certs := &certTable{at: map[coreconsensus.NodeID]time.Time{}}
	var mu sync.Mutex
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 3, 2},
		configure: func(tn *testNode, cfg *Config) {
			mu.Lock()
			defer mu.Unlock()
			if meters[tn.id] == nil {
				meters[tn.id] = svcmetrics.NewMeter()
			}
			cfg.Meter = meters[tn.id]
			cfg.Transport = &certTransport{Transport: cfg.Transport, certs: certs}
		},
	})
	return c, meters, certs
}

// certTable is the peers' certificate expiry a certTransport reports.
type certTable struct {
	mu sync.Mutex
	at map[coreconsensus.NodeID]time.Time
}

// certTransport adds to a memory transport the certificate expiry a TCP
// transport reports.
type certTransport struct {
	coreconsensus.Transport
	certs *certTable
}

func (c *certTransport) CertExpiry() time.Time { return time.Time{} }
func (c *certTransport) ReloadFailures() int   { return 0 }
func (c *certTransport) PeerCertExpiry() map[coreconsensus.NodeID]time.Time {
	c.certs.mu.Lock()
	defer c.certs.mu.Unlock()
	out := map[coreconsensus.NodeID]time.Time{}
	for k, v := range c.certs.at {
		out[k] = v
	}
	return out
}

// A peer's series are reset once the node stops publishing it — the peer
// left, or the node no longer leads: a series is never deleted, and the
// last value stood for good, "peer unreachable" alerting for a member that
// no longer existed (#532).
func TestADepartedPeersSeriesAreReset(t *testing.T) {
	c, meters, _ := meteredCluster(t)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	waitFor(t, "the leader to report the isolated peer unreachable", func() bool {
		v, ok := gaugeAt(meters[l.id], mPeerUnreachable, f.id)
		return ok && v == 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.Remove(ctx, "op:test", f.id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removed peer's unreachable series reset", func() bool {
		v, ok := gaugeAt(meters[l.id], mPeerUnreachable, f.id)
		return ok && v == 0
	})
}

// The variant with no removal: a leader that reported a peer unreachable,
// then lost its leadership, kept reporting it so (#532).
func TestAnExLeaderResetsItsPeersSeries(t *testing.T) {
	c, meters, _ := meteredCluster(t)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var g *testNode
	for _, tn := range c.nodes {
		if tn != l && tn != f {
			g = tn
		}
	}
	c.net.Isolate(f.id, true)
	waitFor(t, "the leader to report the isolated peer unreachable", func() bool {
		v, ok := gaugeAt(meters[l.id], mPeerUnreachable, f.id)
		return ok && v == 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.TransferLeadership(ctx, "op:test", g.id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ex-leader's series of the peer reset", func() bool {
		v, ok := gaugeAt(meters[l.id], mPeerUnreachable, f.id)
		return ok && v == 0
	})
}

// A peer whose certificate the transport no longer reports — it left — has
// its expiry series read as never expiring, not left to count down past the
// alert's threshold (#532).
func TestADepartedPeersCertExpiryStopsCountingDown(t *testing.T) {
	c, meters, certs := meteredCluster(t)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	certs.mu.Lock()
	certs.at[f.id] = time.Now().Add(24 * time.Hour)
	certs.mu.Unlock()
	waitFor(t, "the peer's certificate expiry published", func() bool {
		v, ok := gaugeAt(meters[l.id], mPeerCertExpiry, f.id)
		return ok && v > 0 && v < departedCertExpiry
	})
	certs.mu.Lock()
	delete(certs.at, f.id)
	certs.mu.Unlock()
	waitFor(t, "the departed peer's expiry reset", func() bool {
		v, ok := gaugeAt(meters[l.id], mPeerCertExpiry, f.id)
		return ok && v == departedCertExpiry
	})
}

// The TCP transport forgets the certificate of a member that left: it
// kept every peer it had ever served (#532).
func TestTCPForgetsADepartedPeersCertificate(t *testing.T) {
	_, _, ns := newTCPCluster(t)
	l := tcpLeader(t, ns)
	var gone *tcpNode
	waitFor(t, "every peer's certificate seen", func() bool { return len(l.tr.PeerCertExpiry()) == 2 })
	l.tr.mu.Lock()
	var kept []coreconsensus.MemberValue
	for _, m := range l.tr.byID {
		if gone == nil && m.ID != l.id {
			for _, tn := range ns {
				if tn.id == m.ID {
					gone = tn
				}
			}
			continue
		}
		kept = append(kept, m)
	}
	l.tr.mu.Unlock()
	l.tr.SetMembership(1, kept)
	if _, ok := l.tr.PeerCertExpiry()[gone.id]; ok {
		t.Fatalf("the transport still reports the certificate of %d, which left", gone.id)
	}
}
