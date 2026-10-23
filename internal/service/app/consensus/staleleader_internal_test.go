package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// tcpNodes are the engines of ns that run.
func tcpNodes(ns []*tcpNode) []*Node {
	var out []*Node
	for _, tn := range ns {
		if tn.node != nil {
			out = append(out, tn.node)
		}
	}
	return out
}

// restartTCP starts tn again on its log and its address.
func restartTCP(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, tn *tcpNode) {
	t.Helper()
	tn.listen(t, ca)
	tn.start(t, cid)
}

// staleVoterSetup is #542's setup over the real mTLS transport: voters A,
// B and C at protocol 2, A stopped, D joined, promoted and handed the
// leadership. It returns A (stopped), the founders and D.
func staleVoterSetup(t *testing.T) (*testCA, coreconsensus.ClusterID, []*tcpNode, *tcpNode) {
	t.Helper()
	ca, cid, ns := newTCPCluster(t)
	raiseNodes(t, tcpNodes(ns))
	for i := range 5 {
		proposeOnTCPLeader(t, ns, fmt.Sprintf("k%d=%d", i, i))
	}
	a := ns[0]
	if tcpLeader(t, ns) == a {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := a.node.TransferLeadership(ctx, "op:test", ns[1].id)
		cancel()
		if err != nil {
			t.Fatalf("setup: move the leadership off A: %v", err)
		}
	}
	a.stop(t)
	live := ns[1:]
	d := joinTCP(t, ca, cid, live, "site-d")
	proposeOnTCPLeader(t, live, "d=1")
	if !waitKV(t, d, "d", "1", 10*time.Second) {
		t.Fatalf("setup: D never caught up: %+v", d.node.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := tcpLeader(t, live).node.Promote(ctx, "op:test", d.id); err != nil {
		t.Fatalf("setup: promote D: %v", err)
	}
	if err := tcpLeader(t, live).node.TransferLeadership(ctx, "op:test", d.id); err != nil {
		t.Fatalf("setup: transfer to D: %v", err)
	}
	if l := tcpLeader(t, append(append([]*tcpNode(nil), live...), d)); l != d {
		t.Fatalf("setup: D does not lead: %d does", l.id)
	}
	return ca, cid, ns, d
}

// TestHuntR4ProveStaleVoterNewLeader is #542: a voter that was down while
// D was admitted, promoted and handed the leadership comes back with a
// configuration that does not list D. Its transport cut D's appends, B and
// C were followers and sent it nothing, and it stayed pre-candidate for as
// long as D led. B now answers its PreVote with D's record, and A applies a
// write D commits within a bounded time.
func TestHuntR4ProveStaleVoterNewLeader(t *testing.T) {
	ca, cid, ns, d := staleVoterSetup(t)
	a := ns[0]
	restartTCP(t, ca, cid, a)
	all := append(append([]*tcpNode(nil), ns...), d)
	proposeOnTCPLeader(t, all, "b=2")
	if !waitKV(t, a, "b", "2", 4*testTimingFast().ElectionTimeoutMax+5*time.Second) {
		t.Fatalf("the returned voter never applied D's write: %+v", a.node.Status())
	}
	if st := a.node.Status(); st.Leader != d.id {
		t.Fatalf("the returned voter follows %d, want D %d", st.Leader, d.id)
	}
}

// TestHuntR4ProveStaleVoterReplacementThenFailure is #542's strong variant:
// A stopped, D admitted and promoted, C removed and stopped, the leadership
// handed to D, A restarted, then B fails. The configuration is {A, B, D}:
// A and D, two running voters of three, must elect a leader and commit.
// Before the fix A could not hear D and D could not get A's vote: no
// leader, no commit.
func TestHuntR4ProveStaleVoterReplacementThenFailure(t *testing.T) {
	ca, cid, ns, d := staleVoterSetup(t)
	a, b, c := ns[0], ns[1], ns[2]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retryAdmin(t, "remove C", func() error { return d.node.Remove(ctx, "op:test", c.id) })
	c.stop(t)
	restartTCP(t, ca, cid, a)
	// B fails a few election timeouts after A's return, without waiting
	// for A to have caught up: what A learns, it learns meanwhile.
	time.Sleep(3 * testTimingFast().ElectionTimeoutMax)
	b.stop(t)
	two := []*tcpNode{a, d}
	proposeOnTCPLeader(t, two, "after=1")
	for _, tn := range two {
		if !waitKV(t, tn, "after", "1", 10*time.Second) {
			t.Fatalf("%d never applied the write of the two running voters: %+v", tn.id, tn.node.Status())
		}
	}
}

// The memory double's admission model refuses what the TCP transport
// refuses: a frame from a node its receiver's configuration does not
// list. Under it, the returned voter of #542 still learns the leader.
func TestTheMemoryAdmissionModelRefusesAnUnknownSender(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	a, b := NewMemoryTransport(net, 1), NewMemoryTransport(net, 2)
	got := make(chan coreconsensus.NodeID, 2)
	if err := a.Bind(1, func(p coreconsensus.AuthenticatedPeerValue, _ []byte) { got <- p.ID }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	frame := []byte{14, 0, 0, 0, 1, byte(coreconsensus.MsgHeartbeat), 1, 0, 0, 0, 0, 0, 0, 0}
	b.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: MemorySPKI(1)}})
	b.Send(1, frame)
	select {
	case id := <-got:
		t.Fatalf("a frame from %d reached a node that does not admit it", id)
	case <-time.After(100 * time.Millisecond):
	}
	a.SetMembership(1, []coreconsensus.MemberValue{{ID: 2, SPKI: MemorySPKI(2)}})
	b.Send(1, frame)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("a frame from an admitted member was not delivered")
	}
}

// TestHuntR4ProveStaleVoterNewLeaderInMemory is the base case of #542 on
// the memory double with its admission model on, the setup the existing
// suites could not express.
func TestHuntR4ProveStaleVoterNewLeaderInMemory(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 4, 2}, size: 3})
	raiseToProtocol2(t, c)
	l := c.settledLeader(5 * time.Second)
	a := c.otherThan(l)
	c.kill(a)
	d := c.join(t, "slot-d")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	retryAdmin(t, "promote D", func() error { return c.leader(5*time.Second).node.Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "transfer to D", func() error { return c.leader(5*time.Second).node.TransferLeadership(ctx, "op:test", d.id) })
	if l := c.leader(5 * time.Second); l != d {
		t.Fatalf("setup: D does not lead: %d does", l.id)
	}
	c.start(a)
	tok := propose(t, d.node, "b=2")
	for deadline := time.Now().Add(4*testTimingFast().ElectionTimeoutMax + 5*time.Second); a.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the returned voter never applied D's write: %+v", a.node.Status())
		}
	}
	if st := a.node.Status(); st.Leader != d.id {
		t.Fatalf("the returned voter follows %d, want D %d", st.Leader, d.id)
	}
}

// join starts a new node on slot and sends its JOIN through the memory
// network, retried until a member admits it as a learner.
func (c *cluster) join(t *testing.T, slot string) *testNode {
	t.Helper()
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, slot)
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	st := must(tn.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, err := NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"})
		if err == nil {
			return tn
		}
		if ctx.Err() != nil {
			t.Fatalf("join %s: %v", slot, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// retryAdmin runs op until it succeeds, for up to ten seconds: a change
// is refused while the previous one, or a new leader's no-op, is pending,
// and a promotion while the learner has not caught up.
func retryAdmin(t *testing.T, what string, op func() error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := op()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: %s: %v", what, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
