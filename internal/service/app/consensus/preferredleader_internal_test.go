package consensus

import (
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// waitLeaderIs waits until want leads, and fails naming the leader it got.
func waitLeaderIs(t *testing.T, c *cluster, want *testNode, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if want.node != nil && want.node.Status().Role == coreconsensus.RoleLeader {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("leadership stayed on %d for %v; PreferredLeaders names %d", c.leader(time.Second).id, within, want.id)
}

// TestHuntR8ProvePreferredLeaderTransfersAfterElection is #559:
// preferLeader ran once, on the Ready where the node became leader, when
// becomeLeader had just reset every peer's match to 0 below a commit of at
// least 1. The guard never held and nothing called it again, so
// PreferredLeaders never moved leadership. The leader now re-evaluates it
// at its ticks, so leadership reaches the preferred node within a few
// election timeouts whichever node wins.
func TestHuntR8ProvePreferredLeaderTransfersAfterElection(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 9}, size: 3}
	c.configure = func(_ *testNode, cfg *Config) { cfg.PreferredLeaders = []coreconsensus.NodeID{c.nodes[2].id} }
	newClusterWith(t, c)
	preferred := c.nodes[2]
	if c.settledLeader(5*time.Second) == preferred {
		// Make a non-preferred node win: the preferred one is away during
		// the election, then comes back.
		c.kill(preferred)
		c.settledLeader(5 * time.Second)
		c.start(preferred)
	}
	waitLeaderIs(t, c, preferred, 2*time.Second)
	propose(t, preferred.node, "after=transfer")
}

// TestHuntR8ProvePreferredLeaderAfterFullRestart is #559's second proof:
// after a full restart, with the preferred node back last, leadership
// stayed on whichever node the other two elected.
func TestHuntR8ProvePreferredLeaderAfterFullRestart(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 10}, size: 3}
	newClusterWith(t, c)
	propose(t, c.settledLeader(5*time.Second).node, "before=restart")
	preferred := c.otherThan(c.leader(time.Second))
	for _, tn := range c.nodes {
		c.kill(tn)
	}
	c.configure = func(_ *testNode, cfg *Config) { cfg.PreferredLeaders = []coreconsensus.NodeID{preferred.id} }
	for _, tn := range c.nodes {
		if tn != preferred {
			c.start(tn)
		}
	}
	if c.settledLeader(5*time.Second) == preferred {
		t.Fatal("the preferred node led while it was down")
	}
	c.start(preferred)
	waitLeaderIs(t, c, preferred, 2*time.Second)
}
