package consensus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// forwardHold is a node's memory transport that holds back the frames of
// one kind it sends to one peer until released, and lets every other frame
// through.
type forwardHold struct {
	*MemoryTransport
	mu   sync.Mutex
	kind coreconsensus.MessageKind
	to   coreconsensus.NodeID
	held [][]byte
	took chan struct{}
}

func (h *forwardHold) Send(to coreconsensus.NodeID, frame []byte) {
	h.mu.Lock()
	if h.kind != 0 && to == h.to && len(frame) > 5 && coreconsensus.MessageKind(frame[5]) == h.kind {
		h.held = append(h.held, append([]byte(nil), frame...))
		h.mu.Unlock()
		select {
		case h.took <- struct{}{}:
		default:
		}
		return
	}
	h.mu.Unlock()
	h.MemoryTransport.Send(to, frame)
}

// hold starts holding back the frames of kind sent to to.
func (h *forwardHold) hold(kind coreconsensus.MessageKind, to coreconsensus.NodeID) {
	h.mu.Lock()
	h.kind, h.to = kind, to
	h.mu.Unlock()
}

// release stops holding and sends what was held, in order.
func (h *forwardHold) release() {
	h.mu.Lock()
	held, to := h.held, h.to
	h.kind, h.held = 0, nil
	h.mu.Unlock()
	for _, f := range held {
		h.MemoryTransport.Send(to, f)
	}
}

// forwardHoldCluster is a three-node cluster on steady timing whose nodes
// send through a forwardHold each, by id.
func forwardHoldCluster(t *testing.T, cid coreconsensus.ClusterID) (*cluster, func(coreconsensus.NodeID) *forwardHold) {
	var mu sync.Mutex
	holds := map[coreconsensus.NodeID]*forwardHold{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: cid, size: 3, timing: steadyTiming()}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		h := &forwardHold{MemoryTransport: cfg.Transport.(*MemoryTransport), took: make(chan struct{}, 1)}
		holds[tn.id] = h
		cfg.Transport = h
	}
	newClusterWith(t, c)
	return c, func(id coreconsensus.NodeID) *forwardHold {
		mu.Lock()
		defer mu.Unlock()
		return holds[id]
	}
}

// proposeAsync proposes kv on n under a ten-second deadline and hands its
// error back on the channel.
func proposeAsync(n *Node, kv string) <-chan error {
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, err := n.Propose(ctx, []byte(kv))
		done <- err
	}()
	return done
}

// awaitHeld waits until h holds a frame.
func awaitHeld(t *testing.T, h *forwardHold, what string) {
	t.Helper()
	select {
	case <-h.took:
	case <-time.After(5 * time.Second):
		t.Fatalf("setup: %s was never sent", what)
	}
}

// #1440: the leader's answer to a forward, read by the follower once its
// term had moved, was dropped as an older term's message: the forward,
// appended and committed by the leader, was answered OUTCOME_UNKNOWN by
// the sweep. Here the leader hands over to the forwarding follower while
// its answer is held; released, the answer still resolves the forward.
func TestAForwardAnsweredAcrossAChangeOfTermIsResolved(t *testing.T) {
	c, holdOf := forwardHoldCluster(t, coreconsensus.ClusterID{14, 4, 1})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	term := l.node.Status().Term
	lh := holdOf(l.id)
	lh.hold(coreconsensus.MsgForwardResp, f.id)
	done := proposeAsync(f.node, "fwd=1")
	awaitHeld(t, lh, "the leader's answer to the forward")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.TransferLeadership(ctx, "op:test", f.id); err != nil {
		t.Fatalf("setup: transfer to the forwarding follower: %v", err)
	}
	if st := f.node.Status(); st.Role != coreconsensus.RoleLeader || st.Term <= term {
		t.Fatalf("setup: the follower does not lead a later term than %d: %+v", term, st)
	}
	lh.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the forward answered across a change of term: %v, want its outcome", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the forward answered across a change of term was never resolved")
	}
	if v, _ := f.sm.get("fwd"); v != "1" {
		t.Fatalf("the forward resolved as applied is not applied: %q", v)
	}
}

// #1440: a forward that reached its leader once that leader had moved to a
// later term was dropped there as an older term's message, unanswered, and
// the follower's sweep answered OUTCOME_UNKNOWN although nothing was
// appended. The leader, deposed, now answers it NOT_LEADER, naming the
// leader it follows.
func TestAForwardReachingALeaderThatMovedOnIsRefusedNotLeader(t *testing.T) {
	c, holdOf := forwardHoldCluster(t, coreconsensus.ClusterID{14, 4, 2})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var g *testNode
	for _, tn := range c.nodes {
		if tn != l && tn != f {
			g = tn
		}
	}
	term := l.node.Status().Term
	fh := holdOf(f.id)
	fh.hold(coreconsensus.MsgForward, l.id)
	done := proposeAsync(f.node, "fwd=1")
	awaitHeld(t, fh, "the forward")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.TransferLeadership(ctx, "op:test", g.id); err != nil {
		t.Fatalf("setup: transfer to the third node: %v", err)
	}
	if st := l.node.Status(); st.Role == coreconsensus.RoleLeader || st.Term <= term {
		t.Fatalf("setup: the former leader has not moved past term %d: %+v", term, st)
	}
	start := time.Now()
	fh.release()
	select {
	case err := <-done:
		if !errors.Is(err, coreconsensus.NotLeader) || time.Since(start) > 2*time.Second {
			t.Fatalf("a forward reaching a leader that moved on: %v after %v, want NOT_LEADER at once", err, time.Since(start))
		}
		if lead, ok := LeaderHint(err); !ok || lead != g.id {
			t.Fatalf("the refusal names leader %d (%v), want %d: %v", lead, ok, g.id, err)
		}
		if errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
			t.Fatalf("a refused forward answered OUTCOME_UNKNOWN: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the forward reaching a leader that moved on was never answered")
	}
	if v, _ := g.sm.get("fwd"); v != "" {
		t.Fatalf("a forward refused NOT_LEADER was applied: %q", v)
	}
}
