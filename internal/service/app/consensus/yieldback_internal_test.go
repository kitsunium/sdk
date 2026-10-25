package consensus

import (
	"context"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// holdApply blocks tn's machine in Apply until the returned release runs.
func holdApply(tn *testNode) (release func()) {
	tn.sm.mu.Lock()
	var once sync.Once
	return func() { once.Do(tn.sm.mu.Unlock) }
}

// #674: a leader that handed its leadership over because its apply made no
// progress (#554) declines a transfer back, an operator's included: it
// still acknowledges, so a transfer reached it, and it led again with every
// write entering at it hung, until it handed over once more. Once its apply
// moves, it takes one again.
func TestANodeThatHandedOverForAStuckApplyDeclinesATransferBack(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 4}, size: 3})
	l := c.settledLeader(5 * time.Second)
	c.waitApplied(propose(t, l.node, "a=1").Index, 5*time.Second)
	release := holdApply(l)
	defer release()
	// A write entering at the stuck leader puts something to apply.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, _ = l.node.Propose(ctx, []byte("stuck=1"))
	}()
	var x *testNode
	waitFor(t, "the stuck leader hands its leadership over", func() bool {
		for _, tn := range c.nodes {
			if tn != l && tn.node.Status().Role == coreconsensus.RoleLeader {
				x = tn
				return true
			}
		}
		return false
	})
	propose(t, x.node, "b=2")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := x.node.TransferLeadership(ctx, "op:test", l.id)
	cancel()
	if !errs.HasCode(err, coreconsensus.CodeTransferFailed) {
		t.Fatalf("TransferLeadership back to %d, whose apply is still stuck: %v, want TRANSFER_FAILED", l.id, err)
	}
	if l.node.Status().Role == coreconsensus.RoleLeader {
		t.Fatalf("the node whose apply is stuck leads again")
	}
	release()
	c.waitApplied(propose(t, x.node, "c=3").Index, 5*time.Second)
	if refused, err := transferRetried(x.node, l.id); err != nil {
		t.Fatalf("TransferLeadership to %d once its apply moved, after %d refusals: %v", l.id, refused, err)
	}
	propose(t, l.node, "d=4")
}

// #674's other side: with the leader's apply and the first voter's stuck,
// the first voter takes the leadership over, then hands it over in turn.
// The node that handed over first declines it, and the hand-off, which
// used to go to the same most caught-up voter at every try, kept the stuck
// voter leading. The next try goes to another voter.
func TestALeaderWithAStuckApplyHandsOverPastAVoterAlsoStuck(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 5}, size: 3})
	l := c.settledLeader(5 * time.Second)
	// The leader is the first member of the configuration, x the second and
	// y the third: on equal matches a hand-off picks the first, so the
	// leader's goes to x, and x's to the leader, which declines it.
	var byConf []*testNode
	for _, m := range l.node.Membership().Members {
		for _, tn := range c.nodes {
			if tn.id == m.ID {
				byConf = append(byConf, tn)
			}
		}
	}
	if l != byConf[0] {
		if refused, err := transferRetried(l.node, byConf[0].id); err != nil {
			t.Fatalf("setup: TransferLeadership to %d, after %d refusals: %v", byConf[0].id, refused, err)
		}
		l = byConf[0]
	}
	x, y := byConf[1], byConf[2]
	c.waitApplied(propose(t, l.node, "a=1").Index, 5*time.Second)
	defer holdApply(x)()
	defer holdApply(l)()
	// Writes enter at the leader: neither it nor x applies them.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, _ = l.node.Propose(ctx, []byte("stuck=1"))
	}()
	within := 10 * testTimingFast().ElectionTimeoutMax
	deadline := time.Now().Add(within)
	for y.node.Status().Role != coreconsensus.RoleLeader {
		if time.Now().After(deadline) {
			t.Fatalf("%d, the only voter whose apply moves, does not lead after %v: leader %d", y.id, within, c.leader(time.Second).id)
		}
		time.Sleep(5 * time.Millisecond)
	}
	propose(t, y.node, "b=2")
}
