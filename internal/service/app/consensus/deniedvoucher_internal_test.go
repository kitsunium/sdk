package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #773, over real TCP mutual TLS on 127.0.0.1 with in-memory logs (nothing
// is written to disk). A voter (A) down while D, E and F joined and were
// promoted knows {A, B, C}. On D, the operator denies B's pin, the first
// step of the key-compromise runbook: once it commits, every member
// refuses B (#551), which hears no leader and answers no PreVote with a
// record. #603's guard still counted B as A's voucher, at its address under
// the zero pin, and admitted C's removal: A came back asking B, cut off,
// and C, gone, its transport refusing D's key, a live voter out of the
// quorum for as long as D led. C's removal is now refused while A is down:
// A comes back, C answers its PreVote with D's record, A catches up, and
// C's removal then goes through.
func TestTheLastVoucherBesideADeniedPinIsRemovedOnlyOnceTheStaleVoterIsBack(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	a, b, c := ns[0], ns[1], ns[2]
	tcpLeader(t, ns)
	proposeOnTCPLeader(t, ns, "a=1")
	// B leads while A is up, so the guard judges A on the configuration it
	// heard it at, which lists B and C.
	movedLeadTo(t, ns, b)
	proposeOnTCPLeader(t, ns, "a2=1")
	//: A must hold a2 before it goes down.
	if !waitKV(t, a, "a2", "1", 5*time.Second) {
		t.Fatal("setup: A did not apply a2=1")
	}

	// A goes down knowing {A, B, C}. D, E and F join and are promoted.
	a.stop(t)
	live := []*tcpNode{b, c}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	//: D, E and F join one after the other.
	for _, slot := range []string{"site-d", "site-e", "site-f"} {
		j := joinTCP(t, ca, cid, live, slot)
		proposeOnTCPLeader(t, live, "j"+slot+"=1")
		//: the joiner catches up before its promotion.
		if !waitKV(t, j, "j"+slot, "1", 10*time.Second) {
			t.Fatalf("setup: %s never caught up", slot)
		}
		retryAdmin(t, "promote "+slot, func() error { return tcpLeader(t, live).node.Promote(ctx, "op:test", j.id) })
		live = append(live, j)
	}
	d, e, f := live[2], live[3], live[4]
	movedLeadTo(t, live, d)
	proposeOnTCPLeader(t, live, "d=4")

	// B's key is withdrawn: C, D, E and F are 4 of 6 voters.
	retryAdmin(t, "deny B's pin", func() error { return d.node.DenyPin(ctx, "op:test", b.id) })
	live = []*tcpNode{c, d, e, f}
	proposeOnTCPLeader(t, live, "denied=1")

	// remove asks D to remove id again while the change is refused for a
	// reason other than its merits (a pending change, a leader's no-op),
	// and returns the answer on its merits.
	remove := func(id coreconsensus.NodeID) error {
		deadline := time.Now().Add(10 * time.Second)
		//: until an answer on the merits, or the deadline.
		for {
			err := d.node.Remove(ctx, "op:test", id)
			//: admitted, refused on its merits, or out of time.
			if err == nil || errs.HasCode(err, coreconsensus.CodeMembershipInvalid) || time.Now().After(deadline) {
				return err
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// C answers, and is the last member A reaches under a key it pins: its
	// removal is refused.
	//: on B's count, A would come back hearing no one.
	if err := remove(c.id); !errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
		t.Fatalf("Remove(C) while A, down, knows {A, B, C} and B's pin was denied = %v, want MEMBERSHIP_INVALID", err)
	}

	// A comes back at its address: C answers its PreVote with D's record,
	// and A catches up.
	a.listen(t, ca)
	a.start(t, cid)
	proposeOnTCPLeader(t, live, "e=5")
	//: A must catch up through C.
	if !waitKV(t, a, "e", "5", 4*time.Second) {
		t.Fatalf("A did not catch up through C: %+v", a.node.Status())
	}

	// D heard A on a configuration that lists D: C is removed, and A still
	// follows D.
	retryAdmin(t, "remove C", func() error { return remove(c.id) })
	c.stop(t)
	proposeOnTCPLeader(t, []*tcpNode{a, d, e, f}, "g=6")
	//: A must follow D once C is gone.
	if !waitKV(t, a, "g", "6", 4*time.Second) {
		t.Fatalf("A did not apply g=6 once C left: A %+v, D %+v", a.node.Status(), d.node.Status())
	}
}
