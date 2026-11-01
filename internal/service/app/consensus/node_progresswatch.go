package consensus

import (
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// cutAllowance is how many maximum election timeouts a cut on the apply
// goroutine may hold it before watchProgress counts it as a stuck apply: a
// capture or a write that never returns is the failure the watch exists
// for (#554), and a state written slower than that is handed over at most
// once per cut (#593).
const cutAllowance time.Duration = 30

// progressWatch is the leader's watch over its own storage and machine,
// loop-owned: the durable index and the applied index last seen moving,
// and when. A leader whose fdatasync never returns, or whose Apply blocks,
// kept heartbeating, so CheckQuorum held and the followers never
// campaigned, while every write and read entering at it waited for a local
// sync or apply that never came (#554).
type progressWatch struct {
	stable, applied     uint64
	stableAt, appliedAt time.Time
	// ceded is whether this node handed its leadership over because its
	// apply made no progress, and its applied index has not moved from
	// moved since: it declines a transfer to itself meanwhile, on every
	// role (raft's SetApplyStalled, #674).
	ceded bool
	moved uint64
	// to is the voter the last hand-off went to while this node still
	// leads: that hand-off did not complete, and the next one goes to
	// another voter when there is one (#674).
	to coreconsensus.NodeID
}

// watchProgress hands the leadership to the most up-to-date voter once
// this leader's durable index has trailed its log, or its applied index
// its commit, without moving for the maximum election timeout. A
// legitimately slow fdatasync moves within that bound, and the time the
// apply goroutine spends cutting a snapshot is not counted; a transfer already
// under way is left alone, and the clocks restart after each hand-off.
// It runs on the loop, at every tick; a hand-off for a stuck apply is
// remembered across the change of leadership (watchApplied).
func (n *Node) watchProgress(now time.Time) {
	st := n.r.Status()
	w := &n.watch
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	limit := st.Timing.ElectionTimeoutMax
	cutting := n.cutExempt(now, limit)
	n.watchApplied(applied, st.Commit)
	if st.Role != coreconsensus.RoleLeader {
		w.stable, w.applied, w.stableAt, w.appliedAt, w.to = st.Stable, applied, now, now, 0
		return
	}
	if st.Stable != w.stable || st.Stable >= st.Last {
		w.stable, w.stableAt = st.Stable, now
	}
	if applied != w.applied || applied >= st.Commit || cutting {
		// A cut on the apply goroutine, SnapshotPausesApply or a capture,
		// applies nothing while it writes, and is no stuck apply. Every
		// node cuts at the same threshold, so a leader that handed over
		// for it made its successor cut and hand over in turn, one change
		// per cut for as long as writes came (#593). The clock restarts
		// when the cut ends, or when its allowance does.
		w.applied, w.appliedAt = applied, now
	}
	stuckSync, stuckApply := now.Sub(w.stableAt) > limit, now.Sub(w.appliedAt) > limit
	if (!stuckSync && !stuckApply) || st.Transfer != 0 {
		return
	}
	n.handOver(now, &st, applied, stuckApply)
}

// handOver starts the transfer of a leader that makes no progress to the
// most caught-up voter, and restarts the clocks. A voter that handed over
// for its own stuck apply declines it (#674): the one the last hand-off
// went to is passed over, so it does not keep this one leading. A hand-off
// for a stuck apply is remembered until the apply moves (watchApplied).
func (n *Node) handOver(now time.Time, st *raft.StatusValue, applied uint64, stuckApply bool) {
	w := &n.watch
	to := n.mostCaughtUpVoter(st, w.to)
	if to == 0 {
		return
	}
	n.obs.warn("consensus: the leader makes no progress, handing leadership over",
		lattr("to", uint64(to)), lattr("stable", st.Stable), lattr("last", st.Last),
		lattr("applied", applied), lattr("commit", st.Commit))
	if n.r.TransferLeadership(now, to) == 0 {
		n.warnHandOver(to)
	}
	w.stableAt, w.appliedAt, w.to = now, now, to
	if stuckApply && !w.ceded {
		w.ceded, w.moved = true, applied
		n.r.SetApplyStalled(true)
	}
}

// watchApplied ends what a hand-off for a stuck apply started: from that
// hand-off on, the node declines a transfer to itself (raft's
// SetApplyStalled), until its applied index moves or reaches the commit it
// knows. Its log works, so it still acknowledges and holds the commit, and
// its successor's watch of PreferredLeaders handed it the leadership
// straight back: it led almost all the time with its writes refused
// OVERLOADED (#674). The memory is the node's own, kept across the change
// of leadership, since nothing a leader receives says whether a follower
// applies. It runs on every role, at every tick.
func (n *Node) watchApplied(applied, commit uint64) {
	w := &n.watch
	if w.ceded && (applied != w.moved || applied >= commit) {
		w.ceded = false
		n.r.SetApplyStalled(false)
	}
}

// declineNoted logs a TimeoutNow this node's leader sent it while it
// declines one (watchApplied): an operator's transfer to it fails, and the
// line says why.
func (n *Node) declineNoted(m *coreconsensus.MessageValue) {
	if m.Kind != coreconsensus.MsgTimeoutNow || !n.watch.ceded || m.From != n.lastLead || m.Term != n.lastTerm {
		return
	}
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	n.obs.warn("consensus: a leadership transfer to this node is declined, it handed its leadership over for an apply that has not moved since",
		lattr("from", uint64(m.From)), lattr("applied", applied))
}

// mostCaughtUpVoter is the voter other than this node with the highest
// acknowledged index, past passed over when another voter has one; 0 when
// there is none.
func (n *Node) mostCaughtUpVoter(st *raft.StatusValue, past coreconsensus.NodeID) coreconsensus.NodeID {
	var to coreconsensus.NodeID
	var best uint64
	pastFound := false
	for _, m := range st.Conf.Members {
		match, ok := n.voterMatch(m)
		switch {
		case !ok:
		case m.ID == past:
			pastFound = true
		case to == 0 || match > best:
			to, best = m.ID, match
		}
	}
	if to == 0 && pastFound {
		return past
	}
	return to
}

// voterMatch is the acknowledged index of m when m is a voter other than
// this node whose progress this leader holds.
func (n *Node) voterMatch(m coreconsensus.MemberValue) (uint64, bool) {
	if !m.Voter || m.ID == n.stamp.ID {
		return 0, false
	}
	match, _, ok := n.r.Progress(m.ID)
	return match, ok
}

// cutExempt reports whether the apply goroutine is cutting, and has been
// for less than cutAllowance maximum election timeouts.
func (n *Node) cutExempt(now time.Time, etMax time.Duration) bool {
	since := n.cuttingSince.Load()
	return since != 0 && now.Sub(time.Unix(0, since)) < cutAllowance*etMax
}
