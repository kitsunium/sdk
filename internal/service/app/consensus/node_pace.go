package consensus

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// paceSlices is how many reads a second of a paced stream is cut into: a
// read is at most a tenth of the rate, so a stream that waits for its
// budget waits at most a tenth of a second between two reads, its moved
// mark renewed after each, far inside any pin bound (pinAfterStream).
const paceSlices int64 = 10

// Where a waiting peer stands for the snapshot streams' queue (waiterRole).
const (
	// waiterGone no longer waits in raft's snapshot state: it left, or
	// this node no longer leads it.
	waiterGone waiterRole = iota
	// waiterLearner waits as a learner.
	waiterLearner
	// waiterVoter waits as a voter.
	waiterVoter
)

// pacer is a token bucket over the node's clock, shared by every snapshot
// stream it sends: SnapshotStreamBytesPerSecond bounds them all together,
// so two learners joining at once do not take twice the bandwidth the data
// lane was left (#920). It waits on the injected clock, never on the wall
// clock, so the pacing is asserted under a ManualClock. A read takes its
// bytes first and pays after: the budget may go into debt by one read,
// which the wait that follows pays back, and the bucket never holds more
// than one read's worth, so a stream idle for a while does not burst.
type pacer struct {
	clk   clock.Timed
	rate  float64
	burst int64

	mu     sync.Mutex
	tokens float64
	at     time.Time
}

// queuedStream is a peer waiting for a snapshot stream, in the order it
// asked. passed counts the voters chosen while it waited as a learner,
// each a voter that asked after it, whether its stream started or not: a
// learner passed as many times as the configuration has voters besides
// this leader goes before the next voter (#1483).
type queuedStream struct {
	to     coreconsensus.NodeID
	passed int
}

// waiterRole is where a waiting peer stands when a slot frees, read from
// raft at that moment, never when it asked: a learner promoted while it
// waits is served as a voter, a voter removed is dropped (#1483).
type waiterRole uint8

// newPacer is the bucket of rate bytes a second on clk, nil when rate is 0:
// unlimited.
func newPacer(clk clock.Timed, rate int64) *pacer {
	if rate <= 0 || clk == nil {
		return nil
	}
	return &pacer{clk: clk, rate: float64(rate), burst: max(rate/paceSlices, 1), at: clk.Now()}
}

// limit is the size a read of n bytes is cut to.
func (p *pacer) limit(n int) int {
	return int(min(int64(n), p.burst))
}

// charge takes k bytes from the bucket and returns how long the reader
// waits before the next read: 0 while the bucket holds them.
func (p *pacer) charge(k int) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clk.Now()
	if d := now.Sub(p.at); d > 0 {
		p.tokens = min(p.tokens+d.Seconds()*p.rate, float64(p.burst))
	}
	p.at = now
	p.tokens -= float64(k)
	if p.tokens >= 0 {
		return 0
	}
	return time.Duration(math.Ceil(-p.tokens / p.rate * float64(time.Second)))
}

// pay charges k bytes, then waits on the clock until the bucket is out of
// debt, or until ctx ends, whose error it returns.
func (p *pacer) pay(ctx context.Context, k int) error {
	d := p.charge(k)
	if d <= 0 {
		return nil
	}
	t := p.clk.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// streamSnapshots starts the snapshot streams a Ready asks for, through
// the queue: each peer named that has no stream in flight waits, then the
// free slots serve the waiters in nextWaiter's order. A Ready that named a
// learner before a voter, one slot free, streamed the learner first, and
// the voter waited for its whole stream (O1 of #1483's safety review). A
// slot is only free while nobody waits, so the peers that waited before
// these keep their turn. It runs on the loop.
func (n *Node) streamSnapshots(to []coreconsensus.NodeID) {
	if len(to) == 0 {
		return
	}
	for _, id := range to {
		if _, busy := n.sendStop[id]; !busy {
			n.queueStream(id)
		}
	}
	n.serveWaiting()
}

// queueStream records peer to as waiting for a snapshot stream, served
// once a slot is free. It stays in raft's snapshot state, so nothing is
// appended to it, and holds no pin: the newest snapshot at its turn is the
// one it is sent. Asked twice, it waits once. It runs on the loop.
func (n *Node) queueStream(to coreconsensus.NodeID) {
	for i := range n.streamWait {
		if n.streamWait[i].to == to {
			return
		}
	}
	n.streamWait = append(n.streamWait, queuedStream{to: to})
}

// serveWaiting starts the streams of the waiting peers while fewer than
// MaxSnapshotStreams are in flight, in nextWaiter's order: a voter before a
// learner, each as raft holds it when the slot frees. A voter's turn never
// cuts a stream in flight: it waits for one to end like any other. A peer
// that no longer waits in raft's snapshot state — it left, or this node no
// longer leads it — is dropped. It runs on the loop, once a stream ended
// or a Ready asked for streams.
func (n *Node) serveWaiting() {
	for len(n.streamWait) > 0 && len(n.sendStop) < n.cfg.MaxSnapshotStreams {
		role, limit := n.waiterRoles()
		var to coreconsensus.NodeID
		var ok bool
		n.streamWait, to, ok = nextWaiter(n.streamWait, role, limit)
		if !ok {
			return
		}
		n.sendSnapshot(to)
	}
}

// waiterRoles is raft's view of the peers now, for the queue: where each
// stands (waiterRole), and how many voters besides this leader the
// configuration has, the passes a learner waits at most.
func (n *Node) waiterRoles() (func(coreconsensus.NodeID) waiterRole, int) {
	stats := n.r.PeerStats()
	voters := 0
	for i := range stats {
		if stats[i].Flags.Has(raft.StatVoter) {
			voters++
		}
	}
	return func(to coreconsensus.NodeID) waiterRole {
		for i := range stats {
			if stats[i].ID == to {
				return roleOf(stats[i].Flags)
			}
		}
		return waiterGone
	}, voters
}

// roleOf is where a peer of flags stands for the queue.
func roleOf(flags raft.PeerStatFlags) waiterRole {
	switch {
	case !flags.Has(raft.StatSnapshot):
		return waiterGone
	case flags.Has(raft.StatVoter):
		return waiterVoter
	default:
		return waiterLearner
	}
}

// nextWaiter takes from w, the peers waiting in the order they asked, the
// one a freed slot serves, role saying where each stands now (#1483): the
// first voter, unless the first learner was already passed limit times,
// then that learner; the first learner when no voter waits. Among voters,
// and among learners, the first to ask goes first. A waiter that is gone is
// dropped, and each learner that asked before the voter served is passed
// once more. It returns what is left of w, the peer served, and false when
// none is left.
func nextWaiter(w []queuedStream, role func(coreconsensus.NodeID) waiterRole, limit int) ([]queuedStream, coreconsensus.NodeID, bool) {
	w, voter, learner := keepWaiting(w, role)
	pick := learner
	if voter >= 0 && (learner < 0 || w[learner].passed < limit) {
		pick = voter
		// Every waiter before the first voter is a learner it passes.
		for i := range w[:voter] {
			w[i].passed++
		}
	}
	if pick < 0 {
		return w, 0, false
	}
	to := w[pick].to
	return slices.Delete(w, pick, pick+1), to, true
}

// keepWaiting drops from w the waiters role says are gone, in place, and
// returns what is left with the index of its first voter and of its first
// learner, -1 for none.
func keepWaiting(w []queuedStream, role func(coreconsensus.NodeID) waiterRole) (kept []queuedStream, voter, learner int) {
	kept, voter, learner = w[:0], -1, -1
	for _, q := range w {
		r := role(q.to)
		if r == waiterGone {
			continue
		}
		if r == waiterVoter && voter < 0 {
			voter = len(kept)
		}
		if r == waiterLearner && learner < 0 {
			learner = len(kept)
		}
		kept = append(kept, q)
	}
	clear(w[len(kept):])
	return kept, voter, learner
}

// streamRate records the pace of a stream that ended: the bytes the
// transport read in it over the time from its accept to its end.
func (n *Node) streamRate(bytes int64, d time.Duration) {
	if bytes > 0 && d > 0 {
		n.obs.record(mSnapshotStreamRate, float64(bytes)/d.Seconds())
	}
}
