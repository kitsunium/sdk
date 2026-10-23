package consensus

import (
	"context"
	"errors"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// followerOf returns a node of c other than l.
func followerOf(c *cluster, l *testNode) *testNode {
	for _, tn := range c.nodes {
		if tn != l {
			return tn
		}
	}
	return nil
}

// pendingOf counts the reads and forwards f still holds.
func pendingOf(f *testNode) (reads, fwds int) {
	f.node.exec(func() { reads, fwds = len(f.node.reads), len(f.node.forwards) })
	return reads, fwds
}

// A follower's MsgReadIndex or MsgForward lost once by the transport, in a
// cluster whose leadership never changes, was never retried nor swept: the
// caller waited for its own deadline, forever with none, and the waiter
// stayed in the node until the next leader change (#590). The read is now
// resent and answered, the forward answered OUTCOME_UNKNOWN once its grace
// ends, and neither is kept.
func TestALostReadIndexOrForwardIsAnsweredWithoutALeaderChange(t *testing.T) {
	c := newCluster(t, 3, 1000)
	l := c.settledLeader(5 * time.Second)
	f := followerOf(c, l)
	// One read and one forward leave while the follower's link to the
	// leader drops frames, then the link comes back at once.
	c.net.Cut(f.id, l.id, true)
	readErr := make(chan error, 1)
	propErr := make(chan error, 1)
	go func() {
		_, err := f.node.Barrier(context.Background())
		readErr <- err
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, err := f.node.Propose(ctx, []byte("lost=1"))
		propErr <- err
	}()
	time.Sleep(30 * time.Millisecond)
	c.net.Cut(f.id, l.id, false)
	termBefore := l.node.Status().Term
	// The cluster is healthy: a write through the follower succeeds.
	propose(t, f.node, "after=1")
	select {
	case err := <-readErr:
		if err != nil {
			t.Errorf("Barrier after one lost MsgReadIndex, the leader unchanged: %v, want its resend answered", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Barrier(context.Background()) on a follower still blocked 3 s after one lost MsgReadIndex, leader term %d unchanged: %v", termBefore, l.node.Status().Term == termBefore)
	}
	select {
	case err := <-propErr:
		if !errors.Is(err, coreconsensus.OutcomeUnknown) {
			t.Errorf("a lost forward was answered %v, want OUTCOME_UNKNOWN", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lost forward was not answered before its caller's own 3 s deadline")
	}
	time.Sleep(100 * time.Millisecond)
	if reads, fwds := pendingOf(f); reads != 0 || fwds != 0 {
		t.Errorf("after both were answered, the follower still holds %d read waiter(s) and %d forward(s); leader term %d -> %d", reads, fwds, termBefore, l.node.Status().Term)
	}
}

// A read the leader never answers, its resend lost too, is
// LEADERSHIP_UNCONFIRMED two maximum election timeouts after it was asked:
// the ADR's "did not reach a majority in time", not a wait without end
// (#590).
func TestAReadTheLeaderNeverAnswersIsLeadershipUnconfirmed(t *testing.T) {
	c := newCluster(t, 3, 1000)
	l := c.settledLeader(5 * time.Second)
	f := followerOf(c, l)
	c.net.Cut(f.id, l.id, true)
	defer c.net.Cut(f.id, l.id, false)
	done := make(chan error, 1)
	go func() {
		_, err := f.node.Barrier(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, coreconsensus.LeadershipUnconfirmed) {
			t.Errorf("an unanswered read was %v, want LEADERSHIP_UNCONFIRMED", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Barrier(context.Background()) still blocked 3 s after a read no answer reached")
	}
	if reads, _ := pendingOf(f); reads != 0 {
		t.Errorf("the follower still holds %d read waiter(s) after answering it", reads)
	}
}

// A read or a forward whose caller gave up is dropped at the next sweep,
// before its grace ends (#590).
func TestAReadOrForwardItsCallerGaveUpOnIsDropped(t *testing.T) {
	c := newCluster(t, 3, 1000)
	l := c.settledLeader(5 * time.Second)
	f := followerOf(c, l)
	c.net.Cut(f.id, l.id, true)
	defer c.net.Cut(f.id, l.id, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	errc := make(chan error, 2)
	go func() {
		_, err := f.node.Barrier(ctx)
		errc <- err
	}()
	go func() {
		_, _, err := f.node.Propose(ctx, []byte("gone=1"))
		errc <- err
	}()
	<-errc
	<-errc
	// The grace is 2 x 200 ms: 100 ms after the callers left, only their
	// leaving can have dropped the entries.
	time.Sleep(100 * time.Millisecond)
	if reads, fwds := pendingOf(f); reads != 0 || fwds != 0 {
		t.Errorf("the callers gave up, yet the follower still holds %d read waiter(s) and %d forward(s)", reads, fwds)
	}
}
