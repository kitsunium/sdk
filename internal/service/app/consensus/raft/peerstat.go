package raft

import (
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The facts a PeerStatValue carries.
const (
	// StatVoter is set for a voter, clear for a learner.
	StatVoter PeerStatFlags = 1 << iota
	// StatHeard is set once the peer answered in this term; LastHeard is
	// meaningful only then.
	StatHeard
	// StatProbing is set while the leader probes for the peer's match.
	StatProbing
	// StatSnapshot is set while a snapshot is being sent to the peer.
	StatSnapshot
)

// PeerStatValue is the leader's view of one peer, for Health (D13).
type PeerStatValue struct {
	ID        core.NodeID
	Flags     PeerStatFlags
	Match     uint64
	Next      uint64
	Inflight  int
	LastHeard time.Time
}

// PeerStatFlags are a peer's yes/no facts in the leader's view, one bit
// each, read with Has.
type PeerStatFlags uint8

// Has reports whether every fact of x is set.
func (f PeerStatFlags) Has(x PeerStatFlags) bool { return f&x == x }

// statFlag is x when on, else nothing.
func statFlag(x PeerStatFlags, on bool) PeerStatFlags {
	if on {
		return x
	}
	return 0
}
