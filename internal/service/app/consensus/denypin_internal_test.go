package consensus

import (
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTCPDenyPinNeverCutsTheLastQuorum is #530 over the transport that
// enforces pins: with one voter of three down, denying the pin of the other
// live voter, or the leader's own, is refused MEMBERSHIP_INVALID, and the
// cluster still commits. It used to be appended, the transport cut the
// target at once, and the entry waited forever for the acknowledgement it
// had just refused. With every voter up, a DenyPin of a follower commits
// without it, and the cluster still commits.
func TestTCPDenyPinNeverCutsTheLastQuorum(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	l := tcpLeader(t, ns)
	propose(t, l.node, "settle=1")
	d := ns[0]
	if d == l {
		d = ns[1]
	}
	d.stop(t)
	// Past the vote window, the leader no longer counts d as answering.
	time.Sleep(3 * testTimingFast().ElectionTimeoutMin)
	l = tcpLeader(t, ns)
	var f *tcpNode
	for _, tn := range ns {
		if tn != l && tn != d {
			f = tn
		}
	}
	deny := func(target coreconsensus.NodeID) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return l.node.DenyPin(ctx, "op:test", target)
	}
	if err := deny(f.id); !errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
		t.Fatalf("DenyPin of the one live follower with a voter down: %v, want MEMBERSHIP_INVALID", err)
	}
	if err := deny(l.id); !errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
		t.Fatalf("DenyPin of the leader itself: %v, want MEMBERSHIP_INVALID", err)
	}
	propose(t, l.node, "after=1")

	// The control: every voter up, a follower's pin denied, the cluster
	// commits without it.
	d.listen(t, ca)
	d.start(t, cid)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, _ := d.sm.get("after"); v == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restarted voter did not catch up: %+v", d.node.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	l = tcpLeader(t, ns)
	var target *tcpNode
	for _, tn := range ns {
		if tn != l {
			target = tn
		}
	}
	if err := deny(target.id); err != nil {
		t.Fatalf("DenyPin of a follower with every voter up: %v", err)
	}
	propose(t, l.node, "denied=1")
}
