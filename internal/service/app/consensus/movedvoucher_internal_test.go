package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #761, over real TCP mutual TLS on 127.0.0.1 with in-memory logs (nothing
// is written to disk). A voter (A) down while D was admitted and promoted,
// C removed and the leadership handed to D knows B alone of the members
// left: #603's guard let C's removal through on that count, since B answers
// A's PreVote with D's record (#542). UpdateAddress then moved B: A's
// PreVote went to B's old address, where nothing listened, B, a follower,
// never dials A (#748's back link needs a dial of A), and A's transport
// refuses D's key. A was a live voter out of the quorum for as long as D
// led, and once B failed, A and D, two live voters of three, had no leader.
// The move of B is now refused while A may know no other member at the
// address it serves, as its removal is: the operator brings A back, and B
// then moves.

// movedVoucherMove proposes on l the move of id to addr, again while the
// change is refused for a reason other than its merits (a pending change,
// a leader's no-op), and returns the answer on its merits.
func movedVoucherMove(ctx context.Context, l *tcpNode, id coreconsensus.NodeID, addr string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := l.node.UpdateAddress(ctx, "op:test", id, addr)
		if err == nil || errs.HasCode(err, coreconsensus.CodeMembershipInvalid) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestTheLastMemberAStaleVoterKnowsMovesOnlyOnceItIsBack: B's move is
// refused MEMBERSHIP_INVALID while A is down; A comes back, catches up
// through B at the address it knows, and B's move then goes through. Once
// B fails, A and D still have a leader.
func TestTheLastMemberAStaleVoterKnowsMovesOnlyOnceItIsBack(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	tcpLeader(t, ns)
	proposeOnTCPLeader(t, ns, "a=1")
	// B leads while A is up, so the guard judges A on the configuration it
	// heard it at, which lists B.
	movedLeadTo(t, ns, b)
	proposeOnTCPLeader(t, ns, "a2=1")
	if !waitKV(t, a, "a2", "1", 5*time.Second) {
		t.Fatal("setup: A did not apply a2=1")
	}

	// A goes down; D joins and is promoted, C is removed (A still knows
	// B, which stays), and D takes the leadership.
	a.stop(t)
	live := []*tcpNode{b, c}
	proposeOnTCPLeader(t, live, "b=2")
	d := joinTCP(t, ca, cid, live, "site-d")
	proposeOnTCPLeader(t, live, "c=3")
	if !waitKV(t, d, "c", "3", 10*time.Second) {
		t.Fatalf("setup: D never caught up: %+v", d.node.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	retryAdmin(t, "promote D", func() error { return tcpLeader(t, live).node.Promote(ctx, "op:test", d.id) })
	live = []*tcpNode{b, c, d}
	retryAdmin(t, "remove C", func() error { return tcpLeader(t, live).node.Remove(ctx, "op:test", c.id) })
	c.stop(t)
	live = []*tcpNode{b, d}
	movedLeadTo(t, live, d)
	proposeOnTCPLeader(t, live, "d=4")

	// B answers, and is the last member A knows: its move is refused.
	if err := movedVoucherMove(ctx, d, b.id, movedFreeAddr(t)); !errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
		bm, _ := d.node.Membership().Member(b.id)
		t.Fatalf("UpdateAddress(B) while A, down, knows B alone of the members left = %v, want MEMBERSHIP_INVALID: A comes back dialling B at %s, D records it at %s, and A's transport refuses D", err, b.addr, bm.Address)
	}

	// A comes back at its unchanged address: B answers its PreVote with
	// D's record, and A catches up.
	a.listen(t, ca)
	a.start(t, cid)
	all := []*tcpNode{a, b, d}
	proposeOnTCPLeader(t, live, "e=5")
	if !waitKV(t, a, "e", "5", 4*time.Second) {
		t.Fatalf("A did not catch up through B, unmoved: %+v", a.node.Status())
	}

	// D heard A on a configuration that lists D: B moves.
	newB := movedFreeAddr(t)
	retryAdmin(t, "move B", func() error { return movedVoucherMove(ctx, d, b.id, newB) })
	b.stop(t)
	b.addr = newB
	b.listen(t, ca)
	b.start(t, cid)
	proposeOnTCPLeader(t, all, "f=6")
	if !waitKV(t, a, "f", "6", 4*time.Second) || !waitKV(t, b, "f", "6", 4*time.Second) {
		t.Fatalf("A or B did not apply f=6 once B moved: A %+v, B %+v", a.node.Status(), b.node.Status())
	}

	// One more failure: B dies. D and A are two live voters of three.
	b.stop(t)
	elected := false
	for deadline := time.Now().Add(4 * time.Second); !elected && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, n := range []*Node{d.node, a.node} {
			if n.Status().Role != coreconsensus.RoleLeader {
				continue
			}
			// A leader keeps the name until CheckQuorum deposes it: only
			// one that commits a barrier counts.
			bctx, bcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := n.Barrier(bctx)
			bcancel()
			elected = elected || err == nil
		}
	}
	if !elected {
		t.Fatalf("D and A, two live voters of three, had no leader in 4s after B failed: D %+v, A %+v", d.node.Status(), a.node.Status())
	}
}
