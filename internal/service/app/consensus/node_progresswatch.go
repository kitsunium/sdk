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
}

// watchProgress hands the leadership to the most up-to-date voter once
// this leader's durable index has trailed its log, or its applied index
// its commit, without moving for the maximum election timeout. A
// legitimately slow fdatasync moves within that bound, and the time the
// apply goroutine spends cutting a snapshot is not counted; a transfer already
// under way is left alone, and the clocks restart after each hand-off.
// It runs on the loop, at every tick.
func (n *Node) watchProgress(now time.Time) {
	st := n.r.Status()
	w := &n.watch
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	if st.Role != coreconsensus.RoleLeader {
		*w = progressWatch{stable: st.Stable, applied: applied, stableAt: now, appliedAt: now}
		return
	}
	if st.Stable != w.stable || st.Stable >= st.Last {
		w.stable, w.stableAt = st.Stable, now
	}
	if applied != w.applied || applied >= st.Commit || n.cutExempt(now, st.Timing.ElectionTimeoutMax) {
		// A cut on the apply goroutine, SnapshotPausesApply or a capture,
		// applies nothing while it writes, and is no stuck apply. Every
		// node cuts at the same threshold, so a leader that handed over
		// for it made its successor cut and hand over in turn, one change
		// per cut for as long as writes came (#593). The clock restarts
		// when the cut ends, or when its allowance does.
		w.applied, w.appliedAt = applied, now
	}
	limit := st.Timing.ElectionTimeoutMax
	stuckSync, stuckApply := now.Sub(w.stableAt) > limit, now.Sub(w.appliedAt) > limit
	if (!stuckSync && !stuckApply) || st.Transfer != 0 {
		return
	}
	to := n.mostCaughtUpVoter(&st)
	if to == 0 {
		return
	}
	n.obs.warn("consensus: the leader makes no progress, handing leadership over",
		lattr("to", uint64(to)), lattr("stable", st.Stable), lattr("last", st.Last),
		lattr("applied", applied), lattr("commit", st.Commit))
	n.r.TransferLeadership(now, to)
	w.stableAt, w.appliedAt = now, now
}

// mostCaughtUpVoter is the voter other than this node with the highest
// acknowledged index, 0 when there is none.
func (n *Node) mostCaughtUpVoter(st *raft.StatusValue) coreconsensus.NodeID {
	var to coreconsensus.NodeID
	var best uint64
	for _, m := range st.Conf.Members {
		if !m.Voter || m.ID == n.stamp.ID {
			continue
		}
		if match, _, ok := n.r.Progress(m.ID); ok && (to == 0 || match > best) {
			to, best = m.ID, match
		}
	}
	return to
}

// cutExempt reports whether the apply goroutine is cutting, and has been
// for less than cutAllowance maximum election timeouts.
func (n *Node) cutExempt(now time.Time, etMax time.Duration) bool {
	since := n.cuttingSince.Load()
	return since != 0 && now.Sub(time.Unix(0, since)) < cutAllowance*etMax
}
