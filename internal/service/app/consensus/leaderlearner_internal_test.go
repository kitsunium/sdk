package consensus

import (
	"context"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #765, on the memory network with its admission model (nothing touches
// the disk). L joins, then V joins and is promoted while L is still a
// learner: V's configuration lists L as a learner. V goes down; L is
// promoted, D joins and is promoted, and L, handed the leadership, removes
// the founders. #603's guard let the last removal through: every
// configuration that lists V lists L, which stays and leads. Once the
// leadership moved to D, V came back sending its PreVote to the founders
// alone, both gone (a learner is never asked), L, a follower, never dialled
// it, and V's transport refused D: a live voter out of the quorum, and once
// L failed, D and V, two live voters of three, had no leader. A voter now
// vouches only when V would send it a PreVote: the removal of the last
// founder is refused while V is down. V comes back, asks that founder for
// L's record and catches up; the founder is then removed, and after the
// leadership moved to D and L failed, D and V still commit.
func TestTheLeaderAStaleVoterKnowsAsALearnerDoesNotVouchForIt(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 76, 5}, every: 1000, size: 3})
	raiseToProtocol2(t, c)
	founders := slices.Clone(c.nodes)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lead := func() *testNode { return c.leader(5 * time.Second) }

	l := c.join(t, "slot-l")
	v := c.join(t, "slot-v")
	retryAdmin(t, "promote V", func() error { return lead().node.Promote(ctx, "op:test", v.id) })
	tok := propose(t, lead().node, "v-holds=1")
	waitFor(t, "V applied v-holds", func() bool { return v.node.Status().Applied >= tok.Index })
	if m, _ := v.node.Membership().Member(l.id); m.Voter {
		t.Fatal("setup: V's configuration lists L as a voter")
	}
	c.kill(v)
	net.Isolate(v.id, true)

	retryAdmin(t, "promote L", func() error { return lead().node.Promote(ctx, "op:test", l.id) })
	d := c.join(t, "slot-d")
	retryAdmin(t, "promote D", func() error { return lead().node.Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "transfer to L", func() error { return lead().node.TransferLeadership(ctx, "op:test", l.id) })
	waitFor(t, "L leads", func() bool { return l.node.Status().Role == coreconsensus.RoleLeader })

	// The founders but the last go; the last is the only voter V would
	// send a PreVote that stays, and its removal is refused.
	last := founders[len(founders)-1]
	for _, f := range founders[:len(founders)-1] {
		retryAdmin(t, "remove a founder", func() error { return l.node.Remove(ctx, "op:test", f.id) })
		c.kill(f)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := l.node.Remove(ctx, "op:test", last.id)
		if errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
			break
		}
		if err == nil {
			t.Fatalf("the last founder, the only voter V asks, was removed while V, down, lists the leader L as a learner only: the cluster is now %d members", len(l.node.Membership().Members))
		}
		if time.Now().After(deadline) {
			t.Fatalf("removing the last founder while V is down: %v, want MEMBERSHIP_INVALID", err)
		}
	}

	// V comes back: it asks the last founder, which answers with L's
	// record, and catches up; L then hears it at a configuration where L
	// is a voter, and the founder goes.
	net.Isolate(v.id, false)
	c.start(v)
	tok = propose(t, l.node, "back=1")
	waitFor(t, "V caught up under L", func() bool { return v.node.Status().Applied >= tok.Index })
	retryAdmin(t, "remove the last founder once V is back", func() error { return l.node.Remove(ctx, "op:test", last.id) })
	c.kill(last)

	retryAdmin(t, "transfer to D", func() error { return l.node.TransferLeadership(ctx, "op:test", d.id) })
	waitFor(t, "D leads", func() bool { return d.node.Status().Role == coreconsensus.RoleLeader })
	tok = propose(t, d.node, "under-d=1")
	waitFor(t, "V applied under D", func() bool { return v.node.Status().Applied >= tok.Index })

	// One more failure: D and V, two live voters of three, still commit.
	c.kill(l)
	pctx, pcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pcancel()
	for {
		_, _, err := d.node.Propose(pctx, []byte("two=1"))
		if err == nil {
			return
		}
		if pctx.Err() != nil {
			t.Fatalf("D and V, two live voters of three, cannot commit once L failed: %v; D %+v, V %+v", err, d.node.Status(), v.node.Status())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
