package consensus

import (
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

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
// legitimately slow fdatasync moves within that bound; a transfer already
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
	if applied != w.applied || applied >= st.Commit {
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
