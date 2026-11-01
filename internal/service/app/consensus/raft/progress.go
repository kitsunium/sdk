package raft

import "time"

// The facts a progress holds, one bit each in peerFacts.
const (
	// peerProbeSent pauses a probing peer until it answers or a heartbeat
	// passes.
	peerProbeSent peerFacts = 1 << iota
	// peerHeard is clear until the peer answered once in this term.
	peerHeard
	// peerFetching pauses the peer while the driver reads entries the step
	// function evicted.
	peerFetching
	// peerProtocol2 is set while the peer's last message of the term
	// carried FlagProtocol2.
	peerProtocol2
	// peerProtocol3 is set while the peer's last message of the term
	// carried FlagProtocol3.
	peerProtocol3
	// peerSilent is set while the peer's last answer, to an append or a
	// heartbeat, said it is silent (FlagTruncated, #636): it answers, but
	// acknowledges no append, so it counts for no change that must commit
	// (activeMajority, #790).
	peerSilent
)

// peerFacts are a peer's yes/no state in the leader's progress, one bit each.
type peerFacts uint8

// progressState is how the leader feeds a peer (D6).
type progressState uint8

const (
	// stateProbe sends one append at a time until the peer's log matches.
	stateProbe progressState = iota
	// stateReplicate pipelines appends up to the inflight window.
	stateReplicate
	// stateSnapshot waits for a snapshot transfer to end.
	stateSnapshot
)

// progress is the leader's view of one peer.
type progress struct {
	match, next uint64
	state       progressState
	// facts are the peer's yes/no state (peerFacts).
	facts peerFacts
	// pendingSnapshot is the index of the snapshot being sent.
	pendingSnapshot uint64
	// inflight holds the last index of each append in the window, oldest
	// first, and their sizes.
	inflight      []uint64
	inflightBytes []int
	bytes         int
	// lastHeard is the instant of the peer's last message in this term.
	lastHeard time.Time
	// hbMatch is match at the previous heartbeat answer: a replicating
	// peer whose match did not move across two answers while appends are in
	// flight, and whose answer does not report holding the oldest of them,
	// lost one, and is probed again.
	hbMatch uint64
	// heldSince is when a peer whose window did not move first reported
	// holding the oldest append in flight; zero when it does not.
	heldSince time.Time
}

// becomeProbe restarts the peer at one append at a time from its match. Out
// of a snapshot transfer it resumes after the snapshot it was sent, since the
// peer's log starts there.
func (p *progress) becomeProbe() {
	if p.state == stateSnapshot {
		p.next = max(p.match+1, p.pendingSnapshot+1)
	} else {
		p.next = p.match + 1
	}
	p.state = stateProbe
	p.facts &^= peerProbeSent
	p.pendingSnapshot = 0
	p.freeAll()
}

// becomeReplicate pipelines appends from just after the peer's match, with
// an empty window.
func (p *progress) becomeReplicate() {
	p.state = stateReplicate
	p.next = p.match + 1
	p.facts &^= peerProbeSent
	p.freeAll()
}

// becomeSnapshot pauses appends to the peer until the snapshot at index has
// been sent and answered.
func (p *progress) becomeSnapshot(index uint64) {
	p.state = stateSnapshot
	p.pendingSnapshot = index
	p.facts &^= peerProbeSent
	p.freeAll()
}

// freeAll empties the inflight window, keeping its capacity.
func (p *progress) freeAll() {
	p.inflight = p.inflight[:0]
	p.inflightBytes = p.inflightBytes[:0]
	p.bytes = 0
}

// freeTo releases every append of the window through index.
func (p *progress) freeTo(index uint64) {
	n := 0
	for n < len(p.inflight) && p.inflight[n] <= index {
		p.bytes -= p.inflightBytes[n]
		n++
	}
	p.inflight = append(p.inflight[:0], p.inflight[n:]...)
	p.inflightBytes = append(p.inflightBytes[:0], p.inflightBytes[n:]...)
}

// add records an append ending at last, of size bytes, in the window.
func (p *progress) add(last uint64, size int) {
	p.inflight = append(p.inflight, last)
	p.inflightBytes = append(p.inflightBytes, size)
	p.bytes += size
}

// paused reports whether the leader must not send more to the peer now.
func (p *progress) paused(maxInflight, maxBytes int) bool {
	if p.facts&peerFetching != 0 {
		return true
	}
	switch p.state {
	case stateProbe:
		return p.facts&peerProbeSent != 0
	case stateReplicate:
		return len(p.inflight) >= maxInflight || p.bytes >= maxBytes
	default:
		// stateSnapshot: nothing is sent until the transfer ends.
		return true
	}
}

// rejectCounts reports whether a rejection of the append after index still
// answers something outstanding (R2-07): the probe in flight, or an append
// in the window. Any other rejection is stale.
func (p *progress) rejectCounts(index uint64) bool {
	switch p.state {
	case stateProbe:
		return index == p.next-1
	case stateReplicate:
		return index > p.match && index < p.next
	default:
		// stateSnapshot: no append is outstanding during a transfer.
		return false
	}
}

// maybeUpdate records an acknowledgement of n; it reports whether match grew.
func (p *progress) maybeUpdate(n uint64) bool {
	updated := false
	if p.match < n {
		p.match = n
		updated = true
		p.facts &^= peerProbeSent
	}
	p.next = max(p.next, n+1)
	return updated
}
