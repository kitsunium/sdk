package consensus

import (
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// pinAfterStream bounds the pin of a streamed snapshot, in maximum
// election timeouts: a peer that stopped after its install no longer holds
// the log back once it passes.
const pinAfterStream int64 = 10

// sendPin is a snapshot that pins compaction for a peer. While it streams,
// until is zero. Once streamed, the pin follows the peer's match until the
// peer catches up or until passes: the leader only probes the peer after an
// install, and a cut that landed in that window compacted past the entries
// the peer needed next, so it was offered snapshot after snapshot (#550).
type sendPin struct {
	index uint64
	until time.Time
}

// pinAt is the index pin holds compaction back to at now, and false once
// it no longer holds: a streamed snapshot's pin ends when this node no
// longer leads the peer, or when until passed. It runs on the loop.
func (n *Node) pinAt(to coreconsensus.NodeID, pin sendPin, now time.Time) (uint64, bool) {
	if pin.until.IsZero() {
		return pin.index, true
	}
	match, _, ok := n.r.Progress(to)
	if !ok || now.After(pin.until) {
		return 0, false
	}
	return max(pin.index, match), true
}

// watchPins ends, at each tick, the pins of streamed snapshots that no
// longer hold: their peer caught up with the log, left, or until passed.
// Compaction only runs after a cut, so the log is compacted again at once:
// a pin that ended once the writes stopped held it back until the next
// write. Caught up is the peer's match at the last index, never the pin's
// own index: a snapshot cut at the last index — an idle leader's, or one
// cut on request — counted as caught up at the first tick after its
// stream, before the peer answered anything, and the cuts of the writes
// that followed compacted past the entry it needed next (#588).
func (n *Node) watchPins(now time.Time) {
	last := n.r.Status().Last
	ended := false
	for to, pin := range n.sending {
		if pin.until.IsZero() {
			continue
		}
		match, _, _ := n.r.Progress(to)
		if _, held := n.pinAt(to, pin, now); !held || match >= last {
			delete(n.sending, to)
			ended = true
		}
	}
	if ended {
		n.aq.push(applyItem{compact: true})
	}
}
