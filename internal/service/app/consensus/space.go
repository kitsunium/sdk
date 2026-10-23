package consensus

import (
	"context"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The no-space alarm of D17 (R2-29), raised by the node whose disk runs
// short: its log reaching MaxLogBytes less LogReserveBytes, or its
// filesystem's free space falling under LogReserveBytes. Once committed, the
// leader refuses every command NO_SPACE and keeps admitting the engine
// entries, ClearNoSpace among them, so the reserve holds what the repair
// writes. These settings were validated and read nowhere: a full disk
// stopped every node on STORAGE_FAILED at the rotation it could not create,
// at about the same time on all of them (#515).

const (
	// spaceCheckEvery bounds how often the loop reads its filesystem's free
	// space, as the gauges do.
	spaceCheckEvery time.Duration = time.Second
	// spaceRaiseEvery bounds how often the node proposes the alarm while it
	// is not committed: a node with no leader is refused at once.
	spaceRaiseEvery time.Duration = time.Second
	// spaceOperator is the operator an alarm the engine raises is recorded
	// under (D8).
	spaceOperator coreconsensus.Operator = "engine:no-space"
)

// spaceGuard is the guard's state. checkedAt, nextRaise, fsShort and
// learnerShort belong to the event loop; raising is set while a proposal of
// the alarm is in flight, and short while the last check found the disk
// short.
type spaceGuard struct {
	checkedAt time.Time
	nextRaise time.Time
	fsShort   bool
	// learnerShort is set once a learner's short disk was logged, until the
	// disk is no longer short or the node votes.
	learnerShort bool
	raising      atomic.Bool
	short        atomic.Bool
}

// spaceReporter is what a log store tells the guard: the bytes its segments
// occupy and its filesystem's free bytes, -1 where they are not read.
type spaceReporter interface {
	logBytes() int64
	freeBytes() int64
}

// guardSpace proposes the no-space alarm when this node's disk runs short
// and the alarm is not committed, at most every spaceRaiseEvery. It runs on
// the loop, at each tick and each completed write; the proposal runs on a
// goroutine counted in n.wg, which ends once it is answered, or once the
// node stops. A follower's is forwarded to the leader, as any proposal
// (R3-24). A learner raises nothing: it refuses its own proposals
// (refuseShort) and says so. The cluster needs no learner to commit, and a
// learner whose state outgrew the MaxSnapshotBytes it announced cannot cut
// any more while the leader keeps feeding it the log: its log reached its
// quota, its alarm committed, and every write of the cluster was refused
// NO_SPACE until it was removed (#600).
func (n *Node) guardSpace(now time.Time) {
	g := &n.space
	why, short := n.spaceShort(now)
	g.short.Store(short)
	if !short {
		g.learnerShort = false
		return
	}
	if g.raising.Load() || now.Before(g.nextRaise) {
		return
	}
	st := n.r.Status()
	if st.NoSpace != 0 {
		return
	}
	if m, ok := st.Conf.Member(n.stamp.ID); ok && !m.Voter {
		if !g.learnerShort {
			g.learnerShort = true
			n.obs.warn("consensus: this learner's disk is short; it refuses its own proposals and raises no alarm for the cluster", lstr("why", why))
		}
		return
	}
	g.learnerShort = false
	g.nextRaise = now.Add(spaceRaiseEvery)
	g.raising.Store(true)
	n.wg.Add(1)
	go n.raiseNoSpace(why)
}

// spaceShort reports whether this node's disk is short, and why: the log
// past its quota less the reserve, read at every call, or the filesystem's
// free space under the reserve, read at most every spaceCheckEvery.
func (n *Node) spaceShort(now time.Time) (why string, short bool) {
	sr, ok := n.cfg.Log.(spaceReporter)
	if !ok {
		return "", false
	}
	if sr.logBytes()+n.cfg.LogReserveBytes >= n.cfg.MaxLogBytes {
		return "the log reached MaxLogBytes less LogReserveBytes", true
	}
	g := &n.space
	if now.Sub(g.checkedAt) >= spaceCheckEvery {
		g.checkedAt = now
		free := sr.freeBytes()
		g.fsShort = free >= 0 && free < n.cfg.LogReserveBytes
	}
	if g.fsShort {
		return "the filesystem's free space fell under LogReserveBytes", true
	}
	return "", false
}

// refuseShort answers an ordinary proposal NO_SPACE, before anything is
// appended, while this node's own disk is short and the alarm it proposed
// is not committed yet, and reports whether it did: the reserve is what the
// repair writes, and the commands queued behind the alarm used to fill it
// on a slow disk.
func (n *Node) refuseShort(p *proposal) bool {
	if p.admin || !n.space.short.Load() {
		return false
	}
	p.done <- propResult{err: origin(coreconsensus.NoSpace, errs.String("why", "this node's disk is short"))}
	return true
}

// raiseNoSpace proposes the alarm and logs why; admin logs its outcome.
func (n *Node) raiseNoSpace(why string) {
	defer n.wg.Done()
	defer n.space.raising.Store(false)
	n.obs.warn("consensus: raising the no-space alarm", lstr("why", why))
	ctx, cancel := context.WithTimeout(context.Background(), 2*n.Status().Timing.ElectionTimeoutMax+time.Second)
	defer cancel()
	// The outcome is logged by admin; a refusal is proposed again at the
	// next check while the disk stays short.
	abandon(n.admin(ctx, coreconsensus.EntryNoSpaceRaise, wire.AdminValue{Operator: string(spaceOperator)}))
}

// watchUnfed finds, at each tick while this node leads, the learners whose
// announced MaxSnapshotBytes the newest snapshot is over, and handleOne
// sends them no entries. Such a learner cuts nothing, its own cuts held to
// its own limit, and installs nothing, the leader withholding the
// snapshot: the log it was fed was never compacted, passed its quota, and
// grew until its filesystem was full and it stopped STORAGE_FAILED (#613).
// Its log now stops within about one cut of the state outgrowing its
// limit; it stays a learner the leader names in its log, until the state
// fits again, when the leader offers it a snapshot, or it is removed.
// Heartbeats and empty appends still reach it.
func (n *Node) watchUnfed() {
	st := n.r.Status()
	if st.Role != coreconsensus.RoleLeader {
		clear(n.unfed)
		return
	}
	n.snapState.mu.Lock()
	newest := n.snapState.newestBytes
	n.snapState.mu.Unlock()
	for id := range n.unfed {
		if m, ok := st.Conf.Member(id); !ok || m.Voter {
			delete(n.unfed, id)
		}
	}
	for _, m := range st.Conf.Members {
		over := !m.Voter && m.MaxSnapshotBytes > 0 && newest > m.MaxSnapshotBytes
		switch {
		case over && !n.unfed[m.ID]:
			n.unfed[m.ID] = true
			n.obs.warn("consensus: the state outgrew a learner's announced snapshot limit; it is sent no more entries until it fits or is removed",
				lattr("peer", uint64(m.ID)), lattr("bytes", uint64(newest)), lattr("limit", uint64(m.MaxSnapshotBytes)))
		case !over && n.unfed[m.ID]:
			delete(n.unfed, m.ID)
			n.obs.info("consensus: a learner's announced snapshot limit fits the state again; it is fed the log", lattr("peer", uint64(m.ID)))
		}
	}
}
