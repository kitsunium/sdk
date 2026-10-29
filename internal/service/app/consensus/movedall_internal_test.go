package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #748, over real TCP mutual TLS on 127.0.0.1 with in-memory logs (nothing
// is written to disk). A voter (C) down while UpdateAddress moved every
// other voter (A, then B: D20's runbook, one UpdateAddress per node, run
// over the members that are up) comes back with a configuration that names
// both at their old addresses. #724's remedy has C ask the other members
// of its configuration for the leader's record; here every one of them is
// dialled at a dead address too, so the question never arrived, no record
// came back, and C stayed a live voter out of the quorum for as long as
// the cluster ran. One more failure left two live voters of three with no
// leader. C now answers on the connection the leader dialled it on.

// movedAllMove records a new address for moved through the leader l
// (UpdateAddress, ADR 0152 D8), then restarts moved listening there, as an
// address migration does (D20).
func movedAllMove(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, l, moved *tcpNode) {
	t.Helper()
	newAddr := movedFreeAddr(t)
	upd := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		upd <- l.node.UpdateAddress(ctx, "op:test", moved.id, newAddr)
	}()
	time.Sleep(300 * time.Millisecond)
	moved.stop(t)
	moved.addr = newAddr
	moved.listen(t, ca)
	moved.start(t, cid)
	if err := <-upd; err != nil {
		t.Fatalf("UpdateAddress(%d): %v", moved.id, err)
	}
	if m, _ := l.node.Membership().Member(moved.id); m.Address != newAddr {
		t.Fatalf("setup: the leader names %d at %q, want %q", moved.id, m.Address, newAddr)
	}
}

// TestTheVoterDownWhileEveryOtherVoterMovedCatchesUp: C returns at its
// unchanged address after A and B both moved, catches up under the moved
// leader, and once the voter that does not lead fails, the leader and C
// still commit; that voter returns and C keeps up. only-A-moved is the
// control, #724's case: B, unmoved, vouches for A's address.
func TestTheVoterDownWhileEveryOtherVoterMovedCatchesUp(t *testing.T) {
	for _, moveB := range []bool{false, true} {
		name := "only-A-moved"
		if moveB {
			name = "A-and-B-moved"
		}
		t.Run(name, func(t *testing.T) { movedAll(t, moveB) })
	}
}

func movedAll(t *testing.T, moveB bool) {
	ca, cid, ns := newTCPCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	tcpLeader(t, ns)
	proposeOnTCPLeader(t, ns, "a=1")
	movedLeadTo(t, ns, b)
	oldA, oldB := a.addr, b.addr

	// C goes down for the whole migration.
	c.stop(t)
	proposeOnTCPLeader(t, ns, "b=2")

	// A moves under leader B; then, when moveB, B moves under leader A.
	movedAllMove(t, ca, cid, b, a)
	if moveB {
		movedLeadTo(t, ns, a)
		movedAllMove(t, ca, cid, a, b)
	}
	proposeOnTCPLeader(t, ns, "c=3")
	l := tcpLeader(t, ns)
	if !waitKV(t, a, "c", "3", 5*time.Second) || !waitKV(t, b, "c", "3", 5*time.Second) {
		t.Fatal("setup: A or B did not apply c=3")
	}
	t.Logf("A %s -> %s, B %s -> %s; leader %d", oldA, a.addr, oldB, b.addr, l.id)

	// C returns at its unchanged address.
	c.listen(t, ca)
	c.start(t, cid)
	proposeOnTCPLeader(t, ns, "d=4")
	caught := waitKV(t, c, "d", "4", 4*time.Second)
	dialA, _, _ := c.tr.address(a.id)
	dialB, _, _ := c.tr.address(b.id)
	t.Logf("C dials A at %s (listens at %s), B at %s (listens at %s); C %+v", dialA, a.addr, dialB, b.addr, c.node.Status())
	if !moveB {
		if !caught {
			t.Fatalf("control: with B unmoved, C never caught up in 4s")
		}
		return
	}
	switch {
	case !caught:
		t.Errorf("C, a live voter, never caught up in 4s (20 x ElectionTimeoutMax): every member it can ask was moved, it dials A at %s and B at %s, where nothing listens", dialA, dialB)
	case dialA != a.addr || dialB != b.addr:
		t.Errorf("C caught up dialling A at %s and B at %s, want %s and %s", dialA, dialB, a.addr, b.addr)
	}

	// One more failure: the voter that does not lead dies. The leader and
	// C are two live voters of three.
	l = tcpLeader(t, ns)
	other := a
	if l == a {
		other = b
	}
	other.stop(t)
	elected := false
	for deadline := time.Now().Add(4 * time.Second); !elected && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, n := range []*Node{l.node, c.node} {
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
		t.Errorf("%d and C, two live voters of three, had no leader in 4s after %d failed: %+v, C %+v", l.id, other.id, l.node.Status(), c.node.Status())
	}

	// The failed voter returns at its new address, and C keeps up.
	other.listen(t, ca)
	other.start(t, cid)
	proposeOnTCPLeader(t, ns, "e=5")
	if !waitKV(t, c, "e", "5", 4*time.Second) {
		t.Errorf("after %d returned, C did not keep up: %+v", other.id, c.node.Status())
	}
}

// TestTheMovedVoterAndTheStaleVoterElectALeaderWhileTheThirdIsDown: B
// fails after A moved and before C returns, #724's case left to a
// decision. Nobody can vouch for A's address, and C answers every frame of
// A on the connection A dials: A and C, two live voters of three, elect a
// leader that commits, C catches up, and once B returns C keeps up.
func TestTheMovedVoterAndTheStaleVoterElectALeaderWhileTheThirdIsDown(t *testing.T) {
	ca, cid, ns := movedCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	proposeOnTCPLeader(t, ns, "c=3")
	if !waitKV(t, a, "c", "3", 5*time.Second) {
		t.Fatal("setup: A did not apply c=3")
	}
	b.stop(t)

	c.listen(t, ca)
	c.start(t, cid)
	var l *Node
	for deadline := time.Now().Add(4 * time.Second); l == nil && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, n := range []*Node{a.node, c.node} {
			if n.Status().Role != coreconsensus.RoleLeader {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			if _, err := n.Barrier(ctx); err == nil {
				l = n
			}
			cancel()
		}
	}
	cm, _ := c.node.Membership().Member(a.id)
	if l == nil {
		t.Fatalf("A and C, two live voters of three, had no leader in 4s with B down: C names A at %s, A listens at %s; A %+v, C %+v", cm.Address, a.addr, a.node.Status(), c.node.Status())
	}
	proposeOnTCPLeader(t, []*tcpNode{a, c}, "d=4")
	if !waitKV(t, c, "d", "4", 4*time.Second) {
		t.Fatalf("C did not catch up under %d: %+v", l.Status().ID, c.node.Status())
	}
	if cm, _ = c.node.Membership().Member(a.id); cm.Address != a.addr {
		t.Fatalf("C caught up naming A at %s, want %s", cm.Address, a.addr)
	}

	b.listen(t, ca)
	b.start(t, cid)
	proposeOnTCPLeader(t, ns, "e=5")
	if !waitKV(t, c, "e", "5", 4*time.Second) || !waitKV(t, b, "e", "5", 4*time.Second) {
		t.Fatalf("B or C did not keep up once B returned: B %+v, C %+v", b.node.Status(), c.node.Status())
	}
}
