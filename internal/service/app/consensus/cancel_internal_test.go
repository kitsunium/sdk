package consensus

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestAProposeCancelledWhileQueuedIsNeverAppended is #519: Propose answers
// ctx.Err() only for a proposal that is certainly not appended. A proposal
// queued in propc while the loop is busy, whose caller's context ends before
// the loop takes it, was answered ctx.Err() and appended afterwards, then
// committed and applied on every node: a caller trusting the contract
// applied its command twice. On the leader and on a follower, it is now
// either ctx.Err() and never applied, or OUTCOME_UNKNOWN.
func TestAProposeCancelledWhileQueuedIsNeverAppended(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(10 * time.Second)
	for _, on := range []struct {
		name string
		n    *testNode
	}{{"leader", l}, {"follower", c.otherThan(l)}} {
		key := "ghost-" + on.name
		started, release := make(chan struct{}), make(chan struct{})
		go on.n.node.exec(func() { close(started); <-release })
		<-started
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, _, err := on.n.node.Propose(ctx, []byte(key+"=1"))
		cancel()
		close(release)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: a Propose queued behind a busy loop answered %v, want its context's error", on.name, err)
		}
		tok := proposeOnLeader(t, c, "after-"+on.name+"=1")
		c.waitApplied(tok.Index, 5*time.Second)
		for _, tn := range c.nodes {
			if v, ok := tn.sm.get(key); ok {
				t.Fatalf("%s: node %d applied %s=%s although Propose answered ctx.Err()", on.name, tn.id, key, v)
			}
		}
	}
}

// TestAProposeRacingItsDeadlineIsAnsweredTruthfully runs the submit-entry
// race of #519 many times: a context that ends while the proposal is handed
// over. An answer that says nothing was appended — ctx.Err(), NOT_LEADER —
// is never given for a command that is applied; the others are a success or
// OUTCOME_UNKNOWN.
func TestAProposeRacingItsDeadlineIsAnsweredTruthfully(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(10 * time.Second)
	cancelled := map[string]bool{}
	for i := range 200 {
		key := fmt.Sprintf("race%d", i)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%50)*20*time.Microsecond)
		_, _, err := l.node.Propose(ctx, []byte(key+"=1"))
		cancel()
		switch {
		case err == nil, errs.HasCode(err, coreconsensus.CodeOutcomeUnknown):
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, coreconsensus.NotLeader):
			cancelled[key] = true
		default:
			t.Fatalf("Propose %s: %v", key, err)
		}
	}
	tok := proposeOnLeader(t, c, "after=1")
	c.waitApplied(tok.Index, 5*time.Second)
	for key := range cancelled {
		for _, tn := range c.nodes {
			if _, ok := tn.sm.get(key); ok {
				t.Fatalf("node %d applied %s although Propose answered that nothing was appended (%d of 200 answered so)", tn.id, key, len(cancelled))
			}
		}
	}
	t.Logf("%d of 200 proposals answered that nothing was appended, none applied", len(cancelled))
}
