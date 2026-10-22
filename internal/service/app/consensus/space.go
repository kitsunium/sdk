package consensus

import (
	"context"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
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

// spaceGuard is the guard's state. checkedAt and nextRaise belong to the
// event loop; raising is set while a proposal of the alarm is in flight.
type spaceGuard struct {
	checkedAt time.Time
	nextRaise time.Time
	raising   atomic.Bool
}

// spaceReporter is what a log store tells the guard: the bytes its segments
// occupy and its filesystem's free bytes, -1 where they are not read.
type spaceReporter interface {
	logBytes() int64
	freeBytes() int64
}

// guardSpace proposes the no-space alarm when this node's disk runs short
// and the alarm is not committed, at most every spaceRaiseEvery. It runs on
// the loop; the proposal runs on a goroutine counted in n.wg, which ends
// once it is answered, or once the node stops. A follower's is forwarded to
// the leader, as any proposal (R3-24).
func (n *Node) guardSpace(now time.Time) {
	g := &n.space
	if g.raising.Load() || now.Before(g.nextRaise) {
		return
	}
	why, short := n.spaceShort(now)
	if !short || n.r.Status().NoSpace != 0 {
		return
	}
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
	if now.Sub(n.space.checkedAt) < spaceCheckEvery {
		return "", false
	}
	n.space.checkedAt = now
	if free := sr.freeBytes(); free >= 0 && free < n.cfg.LogReserveBytes {
		return "the filesystem's free space fell under LogReserveBytes", true
	}
	return "", false
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
