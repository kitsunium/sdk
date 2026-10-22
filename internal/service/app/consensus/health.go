package consensus

import (
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// The facts a PeerHealthValue carries.
const (
	// PeerVoter is set for a voter, clear for a learner.
	PeerVoter PeerFlags = 1 << iota
	// PeerUnreachable is set when the transport failed to reach the peer
	// since it last answered, or it has not answered within the minimum
	// election timeout.
	PeerUnreachable
	// PeerSnapshot is set while a snapshot is being sent to the peer.
	PeerSnapshot
)

// The answers a HealthValue carries.
const (
	// HealthReady is the readiness answer: running, a leader known,
	// everything known committed applied, and not refusing the current
	// leader's chain (D4).
	HealthReady HealthFlags = 1 << iota
	// HealthHasLeader is set when a leader is known in the current term.
	HealthHasLeader
	// HealthQuorumReachable is set on a leader a majority answered within
	// the minimum election timeout; never elsewhere.
	HealthQuorumReachable
	// HealthStopped is set once the node stopped itself or was closed.
	HealthStopped
	// HealthSnapshotNearLimit is set when the newest snapshot has reached
	// four fifths of SnapshotLimit: the cuts are about to be refused
	// SNAPSHOT_TOO_LARGE (R2-30). Readiness does not depend on it.
	HealthSnapshotNearLimit
)

// PeerHealthValue is the leader's view of one peer (D13), one line of
// HealthValue.Peers. It is taken on the event loop and stale at the next
// turn.
type PeerHealthValue struct {
	// ID is the peer.
	ID coreconsensus.NodeID
	// Flags are the peer's yes/no facts: PeerVoter, PeerUnreachable,
	// PeerSnapshot.
	Flags PeerFlags
	// Match is the highest index the peer holds durably, as acknowledged.
	Match uint64
	// Lag is how far Match is behind the leader's last index.
	Lag uint64
	// Inflight counts the appends in flight to it.
	Inflight int
	// LastContact is how long ago it last answered; negative when it never
	// answered in this term.
	LastContact time.Duration
}

// PeerFlags are a peer's yes/no facts in a health verdict, one bit each,
// read with Has.
type PeerFlags uint8

// Has reports whether every fact of x is set.
func (f PeerFlags) Has(x PeerFlags) bool { return f&x == x }

// HealthFlags are a verdict's yes/no answers, one bit each, read with Has.
type HealthFlags uint8

// Has reports whether every answer of x is set.
func (f HealthFlags) Has(x HealthFlags) bool { return f&x == x }

// set is f with x on or off.
func (f HealthFlags) set(x HealthFlags, on bool) HealthFlags {
	if on {
		return f | x
	}
	return f &^ x
}

// HealthValue is a node's verdict for an orchestrator's probe (D13).
// HealthReady in Flags is the readiness answer; the other fields say why, and
// Peers is filled only on the leader.
type HealthValue struct {
	// Status is the node's view after its last turn.
	Status coreconsensus.StatusValue
	// Flags are the verdict's yes/no answers: HealthReady, HealthHasLeader,
	// HealthQuorumReachable, HealthStopped, HealthSnapshotNearLimit.
	Flags HealthFlags
	// FailureTolerance is how many voters can fail now with a majority
	// left. Only the leader knows it: -1 elsewhere.
	FailureTolerance int
	// ApplyLagEntries is commit minus applied.
	ApplyLagEntries uint64
	// Peers is the leader's view of every peer, nil elsewhere.
	Peers []PeerHealthValue
	// ChainRefusals counts identical chain refusals of this follower at
	// ChainRefusalIndex from ChainRefusalLeader; the node reports itself
	// unready while it refuses (D4, R6-05).
	ChainRefusals      int
	ChainRefusalIndex  uint64
	ChainRefusalLeader coreconsensus.NodeID
	// StopReason says why the node stopped itself.
	StopReason coreconsensus.StopReasonValue
	// DroppedFrames counts frames whose From differed from the
	// authenticated peer (R7-05).
	DroppedFrames int64
	// LongTurns counts the loop turns longer than the drift budget, each of
	// which dropped the lease (D9, R7-20).
	LongTurns int64
	// JoinsRateLimited counts JOINs refused by the one-per-slot rate.
	JoinsRateLimited int64
	// SnapshotBytes is the size of the newest snapshot this node holds,
	// the larger of its encoded and decoded sizes; 0 with none.
	SnapshotBytes int64
	// SnapshotLimit is the size a cut is held to now: this node's
	// MaxSnapshotBytes, or the smallest a member announces when lower
	// (R2-30).
	SnapshotLimit int64
}

// Health returns the node's verdict, computed on the event loop.
func (n *Node) Health() HealthValue {
	st := n.Status()
	h := HealthValue{
		Status: st, Flags: HealthFlags(0).set(HealthStopped, n.stopped.Load()), DroppedFrames: n.droppedFrom.Load(), FailureTolerance: -1,
		LongTurns: n.longTurns.Load(), JoinsRateLimited: n.joinRefused.Load(),
	}
	if r, ok := n.StopReason(); ok {
		h.StopReason = r
	}
	if st.Commit > st.Applied {
		h.ApplyLagEntries = st.Commit - st.Applied
	}
	stopped := h.Flags.Has(HealthStopped)
	h.Flags = h.Flags.set(HealthHasLeader, st.Leader != 0 && !stopped)
	if !stopped {
		n.exec(func() { n.loopHealth(&h) })
	}
	n.snapshotHealth(&h)
	h.Flags = h.Flags.set(HealthReady, isReady(&h, &st))
	return h
}

// snapshotHealth fills the snapshot's size and the limit a cut is held to,
// and warns from four fifths of it: a state that outgrows the smallest
// limit a member announces is cut no more, and the log grows until it is
// (R2-30, #556). The limit is the configuration's in force, read by
// loopHealth; a stopped node reports its own.
func (n *Node) snapshotHealth(h *HealthValue) {
	n.snapState.mu.Lock()
	h.SnapshotBytes = n.snapState.newestBytes
	n.snapState.mu.Unlock()
	if h.SnapshotLimit == 0 {
		h.SnapshotLimit = n.cfg.MaxSnapshotBytes
	}
	h.Flags = h.Flags.set(HealthSnapshotNearLimit, nearLimit(h.SnapshotBytes, h.SnapshotLimit))
}

// isReady is the verdict's Ready: running, a leader known, everything known
// committed applied, and not refusing the current leader's chain (D4).
func isReady(h *HealthValue, st *coreconsensus.StatusValue) bool {
	return !h.Flags.Has(HealthStopped) && h.Flags.Has(HealthHasLeader) && st.Applied >= st.Commit &&
		(h.ChainRefusals == 0 || h.ChainRefusalLeader != st.Leader)
}

// loopHealth fills the part of a verdict only the event loop can read: the
// quorum, the failure tolerance, the chain refusal and, on a leader, every
// peer's view. It runs on the loop, through exec.
func (n *Node) loopHealth(h *HealthValue) {
	now := n.clk.Now()
	h.Flags = h.Flags.set(HealthQuorumReachable, n.r.QuorumActive(now))
	h.FailureTolerance = n.r.FailureTolerance(now)
	h.ChainRefusalLeader, h.ChainRefusalIndex, h.ChainRefusals = n.r.ChainRefusal()
	conf := n.r.Status().Conf
	h.SnapshotLimit = snapshotLimit(n.cfg.MaxSnapshotBytes, &conf)
	last := n.r.Status().Last
	window := n.r.Status().Timing.ElectionTimeoutMin
	for _, p := range n.r.PeerStats() {
		h.Peers = append(h.Peers, n.peerHealth(&p, now, last, window))
	}
}

// peerHealth is one peer's line of a verdict. A peer is unreachable when it
// never answered this term, when its last answer is older than the minimum
// election timeout, or when the transport reported it unreachable since.
func (n *Node) peerHealth(p *raft.PeerStatValue, now time.Time, last uint64, window time.Duration) PeerHealthValue {
	ph := PeerHealthValue{ID: p.ID, Match: p.Match, Inflight: p.Inflight, LastContact: -1}
	if p.Flags.Has(raft.StatVoter) {
		ph.Flags |= PeerVoter
	}
	if p.Flags.Has(raft.StatSnapshot) {
		ph.Flags |= PeerSnapshot
	}
	if last > p.Match {
		ph.Lag = last - p.Match
	}
	heard := p.Flags.Has(raft.StatHeard)
	if heard {
		ph.LastContact = now.Sub(p.LastHeard)
	}
	if n.peerUnreachable(p, heard, ph.LastContact, window) {
		ph.Flags |= PeerUnreachable
	}
	return ph
}

// peerUnreachable reports whether a peer counts as unreachable: it never
// answered this term, its last answer is older than window, or the
// transport reported it unreachable since.
func (n *Node) peerUnreachable(p *raft.PeerStatValue, heard bool, lastContact, window time.Duration) bool {
	u, reported := n.unreachableAt[p.ID]
	return !heard || lastContact >= window || (reported && u.After(p.LastHeard))
}
