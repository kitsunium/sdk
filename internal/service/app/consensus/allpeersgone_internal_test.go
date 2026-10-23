package consensus

import (
	"context"
	"errors"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A is down while both members it knows, B and C, are replaced by D and E.
// A came back as a voter of {A, D, E} whose own configuration lists only B
// and C: no member of it was left to vouch for D (#542 needs one), its
// transport refused D, and once E failed A and D, two running voters of
// three, never committed again (#603). The leader now refuses the removal
// of the last voter A knows while A does not answer; once A is back and
// caught up through C, C is removed, and A and D commit after E fails.
func TestAStaleVoterIsNotLeftWithNoKnownPeer(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 4, 9}, every: 1000, size: 3})
	raiseToProtocol2(t, c)
	l := c.settledLeader(5 * time.Second)
	a := c.otherThan(l)
	var b, cc *testNode
	for _, n := range c.nodes {
		switch {
		case n == a:
		case b == nil:
			b = n
		default:
			cc = n
		}
	}
	c.kill(a)
	// A down drops what is sent to it, as a TCP peer that cannot dial it
	// does: the memory queue would otherwise hand it B's appends on return.
	net.Isolate(a.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d := c.join(t, "slot-d")
	retryAdmin(t, "promote D", func() error { return c.leader(5*time.Second).node.Promote(ctx, "op:test", d.id) })
	e := c.join(t, "slot-e")
	retryAdmin(t, "promote E", func() error { return c.leader(5*time.Second).node.Promote(ctx, "op:test", e.id) })
	retryAdmin(t, "transfer to D", func() error { return c.leader(5*time.Second).node.TransferLeadership(ctx, "op:test", d.id) })
	retryAdmin(t, "remove B", func() error { return d.node.Remove(ctx, "op:test", b.id) })
	c.kill(b)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := d.node.Remove(ctx, "op:test", cc.id)
		if err == nil {
			t.Fatal("C, the last voter the silent A knows, was removed")
		}
		if errors.Is(err, coreconsensus.MembershipInvalid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removing C while A is silent: %v, want MEMBERSHIP_INVALID", err)
		}
	}
	net.Isolate(a.id, false)
	c.start(a)
	tok := propose(t, d.node, "back=1")
	waitFor(t, "A caught up through C", func() bool { return a.node.Status().Applied >= tok.Index })
	retryAdmin(t, "remove C once A answers", func() error { return d.node.Remove(ctx, "op:test", cc.id) })
	c.kill(cc)
	tok = propose(t, d.node, "after=1")
	waitFor(t, "A applied the leader's write", func() bool { return a.node.Status().Applied >= tok.Index })
	c.kill(e)
	pctx, pcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pcancel()
	if _, _, err := d.node.Propose(pctx, []byte("two=1")); err != nil {
		t.Errorf("A and D, two running voters of three, cannot commit: %v", err)
	}
}
