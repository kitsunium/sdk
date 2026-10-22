package consensus

import (
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// leaderContact is what a node that does not lead knows of whether its
// leader hears it: a heartbeat only says the leader reaches this node, and
// a follower whose answers were lost kept that leader, its commit frozen,
// and reported ready (#541); a learner cut off from every voter never
// campaigns and kept its last leader forever (#540). A read answered by the
// leader — a probe, or any read of the node's own — is the proof, with no
// wire change. It is loop-owned.
type leaderContact struct {
	// heard is when the leader named by from last answered a read of this
	// node.
	heard time.Time
	from  coreconsensus.NodeID
	// probeCtx is the context of the probe in flight, 0 for none, and
	// probeAt when it was sent.
	probeCtx uint64
	probeAt  time.Time
}

// probeLeader sends a read to the leader at most every half minimum
// election timeout while this node follows one, so that its answer keeps
// the contact fresh in a quiet cluster. A lease lets the leader answer at
// once; without one the answer waits for the leader's next round.
func (n *Node) probeLeader(now time.Time) {
	if n.lastRole == coreconsensus.RoleLeader || n.lastLead == 0 {
		return
	}
	every := n.r.Status().Timing.ElectionTimeoutMin / 2
	if !n.contact.probeAt.IsZero() && now.Sub(n.contact.probeAt) < every {
		return
	}
	ctx := n.nextID()
	n.contact.probeCtx, n.contact.probeAt = ctx, now
	n.r.ReadIndex(now, ctx, false)
}

// readAnswered notes a read the leader answered: raft takes a read's
// answer only from the leader it asked (#510), so a success proves that
// leader hears this node. It reports whether the answer was a probe's,
// which has no waiter.
func (n *Node) readAnswered(ctx uint64, code errs.Code, now time.Time) bool {
	if code == 0 && n.lastRole != coreconsensus.RoleLeader && n.lastLead != 0 {
		n.contact.heard, n.contact.from = now, n.lastLead
	}
	if ctx != 0 && ctx == n.contact.probeCtx {
		n.contact.probeCtx = 0
		return true
	}
	return false
}

// leaderConfirmed reports whether the leader this node names is one it
// knows hears it: itself, or a leader that answered one of its reads within
// the maximum election timeout. It runs on the loop.
func (n *Node) leaderConfirmed(now time.Time) bool {
	switch {
	case n.lastLead == 0:
		return false
	case n.lastRole == coreconsensus.RoleLeader:
		return true
	}
	return n.contact.from == n.lastLead && now.Sub(n.contact.heard) <= n.r.Status().Timing.ElectionTimeoutMax
}
