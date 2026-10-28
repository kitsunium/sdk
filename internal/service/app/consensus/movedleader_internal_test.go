package consensus

import (
	"context"
	"net"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #724, over real TCP mutual TLS on 127.0.0.1 with in-memory logs (nothing
// is written to disk). A voter (C) down while UpdateAddress moved another
// voter (A) comes back with a configuration that still names A's old
// address. The TCP transport is one-way per connection: C hears A on the
// connection A dials, but answers on its own outbound link, dialled to the
// address C's configuration names. Once A leads, its progress of C starts
// past C's log, its probe is refused by C, and the refusal never reached A:
// A never backed off, never sent C the entry that carries its new address,
// and C, which hears A, never campaigned nor asked for a record. C was a
// live voter out of the quorum for as long as A led, and one more failure
// (B) left A and C, two live voters of three, with no leader. C now asks
// its members for the record of the leader it cannot reach, and dials A at
// the committed address B's record gives, under the key it already pins.

// movedLeadTo makes to the leader of ns, transferring from whoever leads.
func movedLeadTo(t *testing.T, ns []*tcpNode, to *tcpNode) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		l := tcpLeader(t, ns)
		if l == to {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := l.node.TransferLeadership(ctx, "op:test", to.id)
		cancel()
		if err != nil {
			t.Logf("transfer to %d: %v (retrying)", to.id, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("node %d never took the leadership", to.id)
}

// movedFreeAddr is a loopback address nothing listens on right now.
func movedFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// movedCluster is three founders over TCP where A was moved to a new
// address by UpdateAddress while C was down, under leader B. C is still
// down; A listens at its new address.
func movedCluster(t *testing.T) (ca *testCA, cid coreconsensus.ClusterID, ns []*tcpNode) {
	t.Helper()
	ca, cid, ns = newTCPCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	tcpLeader(t, ns)
	proposeOnTCPLeader(t, ns, "a=1")
	// B leads while A moves, so the move deposes no leader.
	movedLeadTo(t, ns, b)

	// C goes down for the whole move.
	c.stop(t)
	proposeOnTCPLeader(t, ns, "b=2")

	// The operator records A's new address (D8: UpdateAddress), then A
	// restarts listening there, as an address migration does (D20).
	newA := movedFreeAddr(t)
	upd := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		upd <- b.node.UpdateAddress(ctx, "op:test", a.id, newA)
	}()
	time.Sleep(300 * time.Millisecond)
	a.stop(t)
	a.addr = newA
	a.listen(t, ca)
	a.start(t, cid)
	if err := <-upd; err != nil {
		t.Fatalf("UpdateAddress: %v", err)
	}
	if m, _ := b.node.Membership().Member(a.id); m.Address != newA {
		t.Fatalf("setup: B's configuration names A at %q, want %q", m.Address, newA)
	}
	return ca, cid, ns
}

// TestTheVoterThatMissedTheLeadersNewAddressCatchesUp: A takes the
// leadership by an operator's transfer, C returns at its unchanged address
// and catches up under A; once B fails, A and C still have a leader, and
// under B again C still follows.
func TestTheVoterThatMissedTheLeadersNewAddressCatchesUp(t *testing.T) {
	ca, cid, ns := movedCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	proposeOnTCPLeader(t, ns, "c=3")
	movedLeadTo(t, ns, a)
	proposeOnTCPLeader(t, ns, "d=4")
	if !waitKV(t, b, "d", "4", 5*time.Second) {
		t.Fatal("setup: B did not apply d=4")
	}

	c.listen(t, ca)
	c.start(t, cid)
	proposeOnTCPLeader(t, ns, "e=5")
	caught := waitKV(t, c, "e", "5", 4*time.Second)
	cm, _ := c.node.Membership().Member(a.id)
	t.Logf("A leads: %v at %s; C sees A at %s; C %+v", a.node.Status().Role == coreconsensus.RoleLeader, a.addr, cm.Address, c.node.Status())
	if !caught {
		t.Fatalf("C, a live voter, never caught up in 4s (20 x ElectionTimeoutMax) while A leads: it answers A at %s", cm.Address)
	}
	if cm.Address != a.addr {
		t.Fatalf("C caught up with a configuration naming A at %s, want %s", cm.Address, a.addr)
	}

	// One more failure: B dies. A and C are two live voters of three.
	b.stop(t)
	elected := false
	for deadline := time.Now().Add(4 * time.Second); !elected && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, n := range []*Node{a.node, c.node} {
			if n.Status().Role != coreconsensus.RoleLeader {
				continue
			}
			// A leader keeps the name until CheckQuorum deposes it: only
			// one that commits a barrier counts.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := n.Barrier(ctx)
			cancel()
			elected = elected || err == nil
		}
	}
	if !elected {
		t.Fatalf("A and C, two live voters of three, had no leader in 4s after B failed: A %+v, C %+v", a.node.Status(), c.node.Status())
	}

	// B comes back and leads; C keeps up under it.
	b.listen(t, ca)
	b.start(t, cid)
	movedLeadTo(t, ns, b)
	proposeOnTCPLeader(t, ns, "f=6")
	if !waitKV(t, c, "f", "6", 10*time.Second) {
		t.Fatalf("C did not keep up under B: %+v", c.node.Status())
	}
}

// TestTheStaleVoterCatchesUpAfterARollingRestartHandsOffToTheMovedNode:
// the same with no operator's transfer to A: B's graceful Close, a rolling
// restart, hands the leadership off to the most caught-up voter, A, since
// C is down, and C comes back afterwards.
func TestTheStaleVoterCatchesUpAfterARollingRestartHandsOffToTheMovedNode(t *testing.T) {
	ca, cid, ns := movedCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	// B leads again if A won the election after its restart.
	movedLeadTo(t, ns, b)
	proposeOnTCPLeader(t, ns, "c=3")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := b.node.Close(ctx); err != nil {
		t.Logf("close B: %v", err)
	}
	cancel()
	b.node = nil
	if err := b.tr.Close(); err != nil {
		t.Logf("close B's transport: %v", err)
	}
	b.listen(t, ca)
	b.start(t, cid)
	if l := tcpLeader(t, ns); l != a {
		t.Skipf("the hand-off went to %d, not A: nothing to show this run", l.id)
	}

	c.listen(t, ca)
	c.start(t, cid)
	proposeOnTCPLeader(t, ns, "d=4")
	caught := waitKV(t, c, "d", "4", 4*time.Second)
	cm, _ := c.node.Membership().Member(a.id)
	t.Logf("A leads: %v at %s; C sees A at %s; C %+v", a.node.Status().Role == coreconsensus.RoleLeader, a.addr, cm.Address, c.node.Status())
	if !caught {
		t.Fatalf("after a rolling restart of B handed the leadership to A, C never caught up in 4s: it answers A at %s", cm.Address)
	}
}
