package raft

import "time"

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
	// probeSent pauses a probing peer until it answers or a heartbeat
	// passes.
	probeSent bool
	// pendingSnapshot is the index of the snapshot being sent.
	pendingSnapshot uint64
	// inflight holds the last index of each append in the window, oldest
	// first, and their sizes.
	inflight      []uint64
	inflightBytes []int
	bytes         int
	// lastHeard is the instant of the peer's last message in this term.
	lastHeard time.Time
	// heard is false until the peer answered once in this term.
	heard bool
	// hbMatch is match at the previous heartbeat answer: a replicating
	// peer whose match did not move across two answers while appends are in
	// flight lost one, and is probed again.
	hbMatch uint64
}

func (p *progress) reset(next uint64) {
	*p = progress{next: next, inflight: p.inflight[:0], inflightBytes: p.inflightBytes[:0]}
}

func (p *progress) becomeProbe() {
	if p.state == stateSnapshot {
		p.next = max(p.match+1, p.pendingSnapshot+1)
	} else {
		p.next = p.match + 1
	}
	p.state = stateProbe
	p.probeSent = false
	p.pendingSnapshot = 0
	p.freeAll()
}

func (p *progress) becomeReplicate() {
	p.state = stateReplicate
	p.next = p.match + 1
	p.probeSent = false
	p.freeAll()
}

func (p *progress) becomeSnapshot(index uint64) {
	p.state = stateSnapshot
	p.pendingSnapshot = index
	p.probeSent = false
	p.freeAll()
}

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

func (p *progress) add(last uint64, size int) {
	p.inflight = append(p.inflight, last)
	p.inflightBytes = append(p.inflightBytes, size)
	p.bytes += size
}

// paused reports whether the leader must not send more to the peer now.
func (p *progress) paused(maxInflight, maxBytes int) bool {
	switch p.state {
	case stateProbe:
		return p.probeSent
	case stateReplicate:
		return len(p.inflight) >= maxInflight || p.bytes >= maxBytes
	}
	return true
}

// maybeUpdate records an acknowledgement of n; it reports whether match grew.
func (p *progress) maybeUpdate(n uint64) bool {
	updated := false
	if p.match < n {
		p.match = n
		updated = true
		p.probeSent = false
	}
	p.next = max(p.next, n+1)
	return updated
}
