package consensus

import (
	"io"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// pinAfterStream bounds the pin of a streamed snapshot, in maximum
// election timeouts: a peer that stopped after its install no longer holds
// the log back once it passes. It bounds the wait for the install's
// verdict as well, from the moment the transport read the last byte.
const pinAfterStream int64 = 10

// sendPin is a snapshot that pins compaction for a peer. While it streams,
// until is zero, and sent is when the transport read the snapshot's last
// byte, in Unix nanoseconds of n.clk, 0 before. Once streamed, the pin
// follows the peer's match until the peer catches up or until passes: the
// leader only probes the peer after an install, and a cut that landed in
// that window compacted past the entries the peer needed next, so it was
// offered snapshot after snapshot (#550).
type sendPin struct {
	index uint64
	until time.Time
	sent  *atomic.Int64
}

// pinAt is the index pin holds compaction back to at now, and false once
// it no longer holds: a streamed snapshot's pin ends when this node no
// longer leads the peer, or when until passed. A stream's pin ends
// pinAfterStream maximum election timeouts after its last byte was read:
// a receiver that is sent every byte and never answers — a stuck disk
// under its spool's sync, a stuck Restore, an apply goroutine held by an
// abandoned SaveSnapshot — pinned this node's compaction for good, and its
// log grew to the NO_SPACE alarm (#602). It runs on the loop.
func (n *Node) pinAt(to coreconsensus.NodeID, pin sendPin, now time.Time) (uint64, bool) {
	if pin.until.IsZero() {
		if pin.sent != nil {
			if at := pin.sent.Load(); at != 0 && now.Sub(time.Unix(0, at)) > time.Duration(pinAfterStream)*n.r.Status().Timing.ElectionTimeoutMax {
				return 0, false
			}
		}
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
		_, held := n.pinAt(to, pin, now)
		if pin.until.IsZero() {
			if !held {
				n.dropPin(to)
				ended = true
			}
			continue
		}
		if match, _, _ := n.r.Progress(to); !held || match >= last {
			delete(n.sending, to)
			ended = true
		}
	}
	if ended {
		n.aq.push(applyItem{compact: true})
	}
}

// dropPin ends the pin of peer to, and cancels its stream when the pin is
// a stream's whose verdict is overdue: the stream ends, raft probes the
// peer again, and a peer that finished its install meanwhile is found
// there. The receiver keeps its spool and its install. It runs on the loop.
func (n *Node) dropPin(to coreconsensus.NodeID) {
	if n.sending[to].until.IsZero() {
		if stop, ok := n.sendStop[to]; ok {
			stop()
		}
	}
	delete(n.sending, to)
}

// sentReader marks sent with n.clk's time once r reads to its end: the
// transport read the snapshot's last byte.
type sentReader struct {
	r    io.Reader
	n    *Node
	sent *atomic.Int64
}

// Read reads r and marks the end of the stream.
func (s *sentReader) Read(p []byte) (int, error) {
	k, err := s.r.Read(p)
	if err == io.EOF {
		s.sent.CompareAndSwap(0, s.n.clk.Now().UnixNano())
	}
	return k, err
}
