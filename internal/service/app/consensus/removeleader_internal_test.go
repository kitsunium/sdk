package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Removing the leader is an operation of any node (Q8), and it first
// transfers leadership (D8). Submitted on a follower, it was forwarded to
// the leader, which refused a removal of itself MEMBERSHIP_INVALID: whether
// it succeeded depended on the node that received the call (#529).
func TestRemovingTheLeaderFromAFollowerTransfersFirst(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 9}, size: 4,
		configure: func(_ *testNode, cfg *Config) { cfg.MinVoters = 3 },
	})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	// The leader commits with three of the four nodes: f may not have heard
	// of it yet, and a follower that knows no leader answers NOT_LEADER.
	waitFor(t, "the follower knows the leader", func() bool {
		return f.node.Status().Leader == l.id
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := f.node.Remove(ctx, "op:test", l.id)
	if err != nil && !errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		t.Fatalf("Remove(leader %d) on follower %d = %v %v", l.id, f.id, err, errs.FieldsOf(err))
	}
	waitFor(t, "the old leader removed", func() bool {
		_, member := f.node.Membership().Member(l.id)
		return !member
	})
	if st := f.node.Status(); st.Leader == l.id {
		t.Fatalf("the removed node still leads: %+v", st)
	}
}
