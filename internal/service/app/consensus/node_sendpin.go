package consensus

import (
	"io"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// pinAfterStream bounds the pin of a streamed snapshot, in maximum
// election timeouts: a peer that stopped after its install no longer holds
// the log back once it passes. It bounds a stream that stopped moving as
// well: the wait for the accept of its offer, and for the install's
// verdict from the moment the transport read the last byte.
const pinAfterStream int64 = 10

// pinScaleMax caps the doublings of a peer's pin bound: at most 64 times
// pinAfterStream maximum election timeouts, about 21 minutes at the
// default 2 s.
const pinScaleMax uint8 = 6

// sendPin is a snapshot that pins compaction for a peer. While it streams,
// until is zero; moved is when the stream last moved, in Unix nanoseconds
// of n.clk: its offer, then the accept, which opens the snapshot, then each
// read of it by the transport; sent is when the transport read the
// snapshot's last byte, 0 before; and lifted is set once the log was tight
// during the stream: the pin holds compaction back no more, for good, and
// the stream goes on (#622). Once streamed, the pin follows the
// peer's match until the peer catches up or until passes: the leader only
// probes the peer after an install, and a cut that landed in that window
// compacted past the entries the peer needed next, so it was offered
// snapshot after snapshot (#550). follow, set once streamed, renews until
// while the peer still catches up (renewPin).
type sendPin struct {
	index  uint64
	until  time.Time
	sent   *atomic.Int64
	moved  *atomic.Int64
	follow *pinFollow
	lifted bool
}

// pinFollow follows the catch-up of a streamed snapshot's peer: at is where
// the peer stood when its pin was last renewed, the snapshot's index at
// first, and lag how far behind the leader's last index the stream's end
// left it. It holds on the pin's side so a pin stays small enough to pass
// by value.
type pinFollow struct {
	at  uint64
	lag uint64
}

// streamedPin is the pin of the snapshot at index once its stream to peer
// to ended at now, the leader's log then at last: it holds a pinBound,
// renewed while the peer catches up (renewPin).
func (n *Node) streamedPin(to coreconsensus.NodeID, index, last uint64, now time.Time) sendPin {
	return sendPin{index: index, until: now.Add(n.pinBound(to)), follow: &pinFollow{at: index, lag: last - min(last, index)}}
}

// catchingUp reports whether the peer, at pos with the leader's log at
// last, still catches up: it moved since the pin was last renewed, and is
// no further behind than the stream's end left it. One slower than the
// writes falls further behind, and would hold the log growing until it is
// tight: only a snapshot brings it back.
func (f *pinFollow) catchingUp(pos, last uint64) bool {
	return pos > f.at && last-min(last, pos) <= f.lag
}

// renewPin renews, once until passed at now, the pin of a streamed snapshot
// to peer to whose peer still catches up (catchingUp): it holds a pinBound
// past now, and records where the peer stands. The bound was fixed from the
// stream's end, whatever the peer's progress: a peer that caught up by
// appends for longer than that, as one that installed a long transfer under
// a steady write rate does, was compacted past, offered the newest snapshot
// again, and fell as far behind again: it never caught up while the writes
// lasted (#771). A peer that stopped moving, or fell further behind, loses
// it at the next bound, and a tight log still lifts it (pinAt). Any other
// pin is returned unchanged. It runs on the loop.
func (n *Node) renewPin(to coreconsensus.NodeID, pin sendPin, now time.Time) sendPin {
	if pin.follow == nil || !now.After(pin.until) {
		return pin
	}
	match, _, ok := n.r.Progress(to)
	pos := max(pin.index, match)
	if !ok || !pin.follow.catchingUp(pos, n.r.Status().Last) {
		return pin
	}
	pin.until, pin.follow.at = now.Add(n.pinBound(to)), pos
	n.sending[to] = pin
	return pin
}

// pinAt is the index pin holds compaction back to at now, and false once
// it no longer holds: a streamed snapshot's pin ends when this node no
// longer leads the peer, or when until passed, which renewPin moves on
// while the peer catches up. A stream's pin ends once
// its stream is due (streamDue), or once it was lifted. No pin holds once
// the log is tight. It runs on the loop.
func (n *Node) pinAt(to coreconsensus.NodeID, pin sendPin, now time.Time) (uint64, bool) {
	if n.logTight() {
		return 0, false
	}
	if pin.until.IsZero() {
		if pin.lifted || n.streamDue(to, pin, now) {
			return 0, false
		}
		return pin.index, true
	}
	match, _, ok := n.r.Progress(to)
	if !ok || now.After(pin.until) {
		return 0, false
	}
	return max(pin.index, match), true
}

// pinBound is how long a stream's pin to peer to holds once its stream
// stopped moving, and a streamed snapshot's pin once its stream ended or
// since its last renewal: pinAfterStream maximum election
// timeouts, doubled each time a stream's pin to that peer fell due while
// the peer still answered, up to pinScaleMax times. The bound alone could
// not tell a receiver stuck in its install from one only slow to restore:
// a Restore longer than the bound saw the log compacted past the snapshot
// while it ran, and under a steady write rate the follower installed
// snapshot after snapshot and never caught up (#610, #550). Each doubling
// costs it one transfer; a stuck receiver holds compaction at most the
// capped bound at a time, and never once the log is tight.
func (n *Node) pinBound(to coreconsensus.NodeID) time.Duration {
	return time.Duration(pinAfterStream) * n.r.Status().Timing.ElectionTimeoutMax << n.pinScale[to]
}

// streamDue reports whether the stream of pin to peer to is to be
// cancelled at now: it has not moved for pinBound, or this node no longer
// leads the peer, which left the configuration. A receiver that is sent
// every byte and never answers — a stuck disk under its spool's sync, a
// stuck Restore, an apply goroutine held by an abandoned SaveSnapshot —
// pinned this node's compaction for good, and its log grew to the
// NO_SPACE alarm (#602). So did one that never answered the offer, its
// loop standing still in its judgment, since the bound ran from the last
// byte only, and one removed while it was sent the snapshot, whose stream
// only its end released (#623). It runs on the loop.
func (n *Node) streamDue(to coreconsensus.NodeID, pin sendPin, now time.Time) bool {
	if _, _, ok := n.r.Progress(to); !ok {
		return true
	}
	return pin.moved != nil && now.Sub(time.Unix(0, pin.moved.Load())) > n.pinBound(to)
}

// logTight reports whether the log is past both of two thresholds: within
// twice LogReserveBytes of MaxLogBytes, and half way to the no-space
// alarm, which rises at MaxLogBytes less LogReserveBytes. No pin holds
// compaction back then, whatever its bound, so a pin never brings the
// alarm on. The first threshold alone covered the whole quota once
// MaxLogBytes was at most twice LogReserveBytes, which refuseCeilings
// accepts — 512 MiB under the default reserve — and no pin ever held: a
// snapshot in flight was compacted past, as before #550 (#622). From three
// reserves up, the first is the later of the two and the band is #616's.
func (n *Node) logTight() bool {
	sr, ok := n.cfg.Log.(spaceReporter)
	if !ok {
		return false
	}
	b := sr.logBytes()
	return b+2*n.cfg.LogReserveBytes >= n.cfg.MaxLogBytes && 2*b >= n.cfg.MaxLogBytes-n.cfg.LogReserveBytes
}

// growPin doubles the pin bound of peer to, whose stream's pin fell due,
// when it answered within the last maximum election timeout: it is alive
// and still installing, slow rather than stuck. Nor while the log is
// tight: no pin holds compaction back then, and a longer bound would hold
// nothing.
func (n *Node) growPin(to coreconsensus.NodeID, now time.Time) {
	_, heard, ok := n.r.Progress(to)
	if !ok || n.logTight() || n.pinScale[to] >= pinScaleMax || now.Sub(heard) >= n.r.Status().Timing.ElectionTimeoutMax {
		return
	}
	n.pinScale[to]++
	n.obs.info("consensus: a snapshot receiver still installing outlasted its pin; the next pin holds longer",
		lattr("peer", uint64(to)), lstr("bound", n.pinBound(to).String()))
}

// watchPins ends, at each tick, the pins of streamed snapshots that no
// longer hold: their peer caught up with the log, left, or until passed
// while it no longer caught up (renewPin); it cancels the streams that are
// due, and lifts the others' hold once the log is tight (releasePin).
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
		pin = n.renewPin(to, pin, now)
		_, held := n.pinAt(to, pin, now)
		if pin.until.IsZero() {
			if !held && n.releasePin(to, pin, now) {
				ended = true
			}
			continue
		}
		if match, _, _ := n.r.Progress(to); !held || match >= last {
			delete(n.sending, to)
			ended = true
		}
	}
	n.prunePinScale()
	if ended {
		n.aq.push(applyItem{compact: true})
	}
}

// prunePinScale forgets the pin bounds of peers this node no longer leads.
func (n *Node) prunePinScale() {
	for to := range n.pinScale {
		if _, _, ok := n.r.Progress(to); !ok {
			delete(n.pinScale, to)
		}
	}
}

// releasePin settles the pin of peer to, which no longer holds compaction
// at now, and reports whether it changed: a stream's pin is dropped, and
// its stream cancelled, only when the stream is due (streamDue); one that
// only lost its hold to a tight log is lifted, for good, and its stream
// goes on. A tight log says nothing of the transfer: cancelled with it, no
// stream outlived the band's first tick, and under a quota of at most
// twice LogReserveBytes no follower behind the compacted log, joiner or
// learner was ever installed (#622). Any other pin is dropped. It runs on
// the loop.
func (n *Node) releasePin(to coreconsensus.NodeID, pin sendPin, now time.Time) bool {
	if pin.until.IsZero() && !n.streamDue(to, pin, now) {
		if pin.lifted {
			return false
		}
		pin.lifted = true
		n.sending[to] = pin
		return true
	}
	n.dropPin(to)
	return true
}

// dropPin ends the pin of peer to, and cancels its stream when the pin is
// a stream's, which releasePin drops only once the stream is due: the
// stream ends, raft probes the peer again, and a peer that finished its
// install meanwhile is found there. The receiver keeps its spool and its
// install, and its next pin holds longer when it still answers (growPin).
// It runs on the loop.
func (n *Node) dropPin(to coreconsensus.NodeID) {
	if n.sending[to].until.IsZero() {
		if stop, ok := n.sendStop[to]; ok {
			stop()
		}
		n.growPin(to, n.clk.Now())
	}
	delete(n.sending, to)
}

// sentReader marks moved with n.clk's time at each read of r, and sent
// once r reads to its end: the transport read the snapshot's last byte.
type sentReader struct {
	r     io.Reader
	n     *Node
	sent  *atomic.Int64
	moved *atomic.Int64
}

// Read reads r, and marks the stream moving and its end.
func (s *sentReader) Read(p []byte) (int, error) {
	k, err := s.r.Read(p)
	now := s.n.clk.Now().UnixNano()
	s.moved.Store(now)
	if err == io.EOF {
		s.sent.CompareAndSwap(0, now)
	}
	return k, err
}
