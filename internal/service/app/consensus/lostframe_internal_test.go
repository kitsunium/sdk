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
//
// The read is resent once, a maximum election timeout after it was sent,
// and answered LEADERSHIP_UNCONFIRMED at two (sweepReads): a read lost
// twice is refused, never held for an election. The test lifted the cut
// from its own goroutine, after polling for the two waiters, and a run
// whose goroutines were late by a maximum election timeout lost the resend
// too, and saw that refusal where it wanted the resend answered (#1381;
// held 250 ms before the lift, the test fails 5 runs of 5 so). The cut is
// now lifted on the follower's loop, the loop that resends, and only while
// the read has not been resent: an attempt whose resend already went out on
// the cut link is held to the refusal, and the next attempt asks again.
func TestALostReadIndexOrForwardIsAnsweredWithoutALeaderChange(t *testing.T) {
	c := newCluster(t, 3, 1000)
	l := c.settledLeader(5 * time.Second)
	f := followerOf(c, l)
	termBefore := l.node.Status().Term
	for attempt := 1; !lostReadAndForward(t, c, l, f); attempt++ {
		if attempt == 5 {
			t.Fatalf("in 5 attempts the follower's loop never ran the lift before the read's resend; leader term %d -> %d", termBefore, l.node.Status().Term)
		}
	}
	// The caller is answered on the loop's turn that forgets the waiter
	// or on the next: the check waits for the node to let both go, where
	// a fixed 100 ms sleep was the synchronisation.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		reads, fwds := pendingOf(f)
		if reads == 0 && fwds == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("5 s after both were answered, the follower still holds %d read waiter(s) and %d forward(s); leader term %d -> %d", reads, fwds, termBefore, l.node.Status().Term)
		}
	}
}

// lostReadAndForward sends one read and one forward from f while its link
// to l drops frames, lifts the cut on f's loop if the read was not resent
// yet, and checks their answers. It reports whether the lift came before
// the resend: when it did not, the read was lost twice, and its answer is
// held to LEADERSHIP_UNCONFIRMED instead.
func lostReadAndForward(t *testing.T, c *cluster, l, f *testNode) bool {
	t.Helper()
	// One read and one forward leave while the follower's link to the
	// leader drops frames.
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
	// Both are registered when the loop sends them, and the memory
	// network drops a frame on a cut link at the send: once both are
	// held, both were lost.
	waitFor(t, "the read and the forward sent on the cut link", func() bool {
		reads, fwds := pendingOf(f)
		return reads == 1 && fwds == 1
	})
	// On the loop, between two of its turns: the resend cannot run while
	// the cut is lifted, so it leaves after the lift or it already left.
	resent := true
	f.node.exec(func() {
		for _, w := range f.node.reads {
			resent = !w.askedAt.Equal(w.sentAt)
		}
		if !resent {
			c.net.Cut(f.id, l.id, false)
		}
	})
	c.net.Cut(f.id, l.id, false)
	termBefore := l.node.Status().Term
	// The cluster is healthy: a write through the follower succeeds.
	propose(t, f.node, "after=1")
	select {
	case err := <-readErr:
		switch {
		case !resent && err != nil:
			t.Errorf("Barrier after one lost MsgReadIndex, the leader unchanged: %v, want its resend answered", err)
		case resent && !errors.Is(err, coreconsensus.LeadershipUnconfirmed):
			t.Errorf("Barrier after a MsgReadIndex and its resend lost: %v, want LEADERSHIP_UNCONFIRMED", err)
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
	return !resent
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
