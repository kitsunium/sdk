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

// queueStream records peer to as waiting for a snapshot stream: the
// streams in flight are at MaxSnapshotStreams. It stays in raft's snapshot
// state, so nothing is appended to it, and holds no pin: the newest
// snapshot at its turn is the one it is sent. Asked twice, it waits once.
// It runs on the loop.
func (n *Node) queueStream(to coreconsensus.NodeID) {
	if slices.Contains(n.streamWait, to) {
		return
	}
	n.streamWait = append(n.streamWait, to)
}

// serveWaiting starts the streams of the waiting peers, first asked first
// served, while fewer than MaxSnapshotStreams are in flight. A peer that no
// longer waits in raft's snapshot state — it left, or this node no longer
// leads it — is dropped. It runs on the loop, once a stream ended.
func (n *Node) serveWaiting() {
	for len(n.streamWait) > 0 && len(n.sendStop) < n.cfg.MaxSnapshotStreams {
		to := n.streamWait[0]
		n.streamWait = n.streamWait[1:]
		if n.awaitsSnapshot(to) {
			n.sendSnapshot(to)
		}
	}
}

// awaitsSnapshot reports whether raft holds peer to in its snapshot state.
func (n *Node) awaitsSnapshot(to coreconsensus.NodeID) bool {
	for _, p := range n.r.PeerStats() {
		if p.ID == to {
			return p.Flags.Has(raft.StatSnapshot)
		}
	}
	return false
}

// streamRate records the pace of a stream that ended: the bytes the
// transport read in it over the time from its accept to its end.
func (n *Node) streamRate(bytes int64, d time.Duration) {
	if bytes > 0 && d > 0 {
		n.obs.record(mSnapshotStreamRate, float64(bytes)/d.Seconds())
	}
}
