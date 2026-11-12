package consensus

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// The client sessions of #918 through the driver: ProposeSession on a
// cluster raised to protocol 4, its blind retry answered with the first
// token, its refusals, and the table carried by a snapshot and by a whole
// restart. The step function's fault schedule is raft's
// TestBlindSessionRetriesAreAppliedOnce.

// sessionPropose is ProposeSession through n, again while n knows no
// leader to forward to.
func sessionPropose(t *testing.T, n *Node, s coreconsensus.SessionID, seq uint64, data string) (coreconsensus.TokenValue, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		var tok coreconsensus.TokenValue
		var err error
		if seq == 0 {
			err = n.OpenSession(ctx, s)
		} else {
			tok, _, err = n.ProposeSession(ctx, s, seq, []byte(data))
		}
		if (errors.Is(err, coreconsensus.NotLeader) || errs.HasCode(err, coreconsensus.CodeOutcomeUnknown)) && ctx.Err() == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		return tok, err
	}
}

func TestASessionCommandIsAppliedOnceThroughRetriesSnapshotsAndRestarts(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(10 * time.Second)
	f := c.otherThan(l)
	sid := coreconsensus.SessionID{0x91, 0x8}
	if _, err := sessionPropose(t, f.node, sid, 0, ""); !errs.HasCode(err, coreconsensus.CodePayloadVersionRefused) {
		t.Fatalf("a session opened below cluster version 4: %v, want PAYLOAD_VERSION_REFUSED", err)
	}
	nodes := []*Node{c.nodes[0].node, c.nodes[1].node, c.nodes[2].node}
	raiseNodesTo(t, nodes, raft.Protocol4)
	if _, err := sessionPropose(t, f.node, sid, 0, ""); err != nil {
		t.Fatalf("open through a follower: %v", err)
	}
	if _, err := sessionPropose(t, l.node, sid, 0, ""); err != nil {
		t.Fatalf("open again: %v", err)
	}
	tok1, err := sessionPropose(t, l.node, sid, 1, "a=1")
	if err != nil {
		t.Fatalf("command 1: %v", err)
	}
	c.waitApplied(tok1.Index, 10*time.Second)
	commit := l.node.Status().Commit
	for _, n := range c.nodes {
		again, err := sessionPropose(t, n.node, sid, 1, "a=1")
		if err != nil || again != tok1 {
			t.Fatalf("command 1 retried through %d: %v, token %v, want %v", n.id, err, again, tok1)
		}
	}
	if _, err := sessionPropose(t, f.node, sid, 1, "a=9"); !errs.HasCode(err, coreconsensus.CodeSessionDigestMismatch) {
		t.Fatalf("command 1 with another payload: %v, want SESSION_DIGEST_MISMATCH", err)
	}
	if got := l.node.Status().Commit; got != commit {
		t.Fatalf("the copies and the refusal appended %d entries", got-commit)
	}
	if _, _, err := l.node.ProposeSession(context.Background(), sid, 0, nil); !errs.HasCode(err, coreconsensus.CodeSessionExpired) {
		t.Fatalf("a command of sequence 0: %v, want SESSION_EXPIRED", err)
	}
	if _, err := sessionPropose(t, l.node, coreconsensus.SessionID{0x91, 0x9}, 1, "b=1"); !errs.HasCode(err, coreconsensus.CodeSessionExpired) {
		t.Fatalf("a session never opened: %v, want SESSION_EXPIRED", err)
	}
	tok2, err := sessionPropose(t, l.node, sid, 2, "a=2")
	if err != nil {
		t.Fatalf("command 2: %v", err)
	}
	c.waitApplied(tok2.Index, 10*time.Second)
	if _, err := sessionPropose(t, l.node, sid, 1, "a=1"); !errs.HasCode(err, coreconsensus.CodeSessionExpired) {
		t.Fatalf("command 1 below the session's 2: %v, want SESSION_EXPIRED", err)
	}

	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	meta, err := l.node.SaveSnapshot(ctx, "op:save", &buf)
	cancel()
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(meta.Sessions) != 1 || meta.Sessions[0].ID != sid || meta.Sessions[0].Seq != 2 || meta.Sessions[0].Position != tok2.Position() {
		t.Fatalf("the snapshot records the sessions %+v, want session %x at sequence 2, at %v", meta.Sessions, sid[:2], tok2.Position())
	}

	for _, tn := range c.nodes {
		c.kill(tn)
	}
	for _, tn := range c.nodes {
		c.start(tn)
	}
	l = c.settledLeader(10 * time.Second)
	commit = l.node.Status().Commit
	again, err := sessionPropose(t, c.otherThan(l).node, sid, 2, "a=2")
	if err != nil || again != tok2 {
		t.Fatalf("command 2 retried after a whole restart: %v, token %v, want %v", err, again, tok2)
	}
	if got := l.node.Status().Commit; got != commit {
		t.Fatalf("the retry after the restart appended %d entries", got-commit)
	}
	tok3, err := sessionPropose(t, l.node, sid, 3, "a=3")
	if err != nil {
		t.Fatalf("command 3 after the restart: %v", err)
	}
	c.waitApplied(tok3.Index, 10*time.Second)
	for _, tn := range c.nodes {
		if v, _ := tn.sm.get("a"); v != "3" {
			t.Fatalf("node %d holds a=%q, want 3", tn.id, v)
		}
	}
}
