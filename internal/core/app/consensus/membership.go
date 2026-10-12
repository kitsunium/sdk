package consensus

import "time"

// Member is one node of the membership: its identity, the key it is pinned
// to, where it listens, and whether it votes.
type Member struct {
	// ID is the node.
	ID NodeID
	// Incarnation is the node's data-directory incarnation.
	Incarnation Incarnation
	// Slot is the placement slot its certificate names.
	Slot string
	// SPKI is the SHA-256 of its certificate's SubjectPublicKeyInfo. A zero
	// pin admits nobody: it is what DenyPin leaves.
	SPKI [32]byte
	// Address is where its transport listens.
	Address string
	// Voter is true for a voter, false for a learner.
	Voter bool
	// Joining marks an empty learner admitted before it holds any log.
	Joining bool
	// PayloadMax is the highest payload version its state machine supports; 0
	// means only the lowest.
	PayloadMax uint32
	// MaxSnapshotBytes is the largest snapshot it accepts, 0 when unknown.
	MaxSnapshotBytes int64
}

// Membership is the configuration at a position: its members and the set of
// retired IDs, which is never reused.
type Membership struct {
	// Index is the index of the entry that produced it, 0 at founding.
	Index uint64
	// Members lists every voter and learner.
	Members []Member
	// Retired lists every removed NodeID.
	Retired []NodeID
}

// Member returns the member with that id.
func (m Membership) Member(id NodeID) (Member, bool) {
	for _, x := range m.Members {
		if x.ID == id {
			return x, true
		}
	}
	return Member{}, false
}

// IsRetired reports whether the id was removed.
func (m Membership) IsRetired(id NodeID) bool {
	for _, x := range m.Retired {
		if x == id {
			return true
		}
	}
	return false
}

// Voters returns the ids of the voters, in membership order.
func (m Membership) Voters() []NodeID {
	out := make([]NodeID, 0, len(m.Members))
	for _, x := range m.Members {
		if x.Voter {
			out = append(out, x.ID)
		}
	}
	return out
}

// Clone returns a deep copy, so that a membership handed out cannot be changed
// under the engine.
func (m Membership) Clone() Membership {
	return Membership{
		Index:   m.Index,
		Members: append([]Member(nil), m.Members...),
		Retired: append([]NodeID(nil), m.Retired...),
	}
}

// TimingContract is the set of timings every member must agree on for leases
// and vote refusals to be sound. The committed contract entry is its ONLY
// authority; a node's own configuration is merely the proposal used at
// Bootstrap.
type TimingContract struct {
	// Generation counts the committed contracts, 1 at founding.
	Generation uint64
	// HeartbeatInterval is how often the leader confirms itself.
	HeartbeatInterval time.Duration
	// ElectionTimeoutMin is the lower bound of the randomized election
	// timeout, and the vote-refusal window.
	ElectionTimeoutMin time.Duration
	// ElectionTimeoutMax is its upper bound, exclusive.
	ElectionTimeoutMax time.Duration
	// MaxClockDrift is the largest rate difference between two clocks, as a
	// fraction in (0, 1). Zero refuses lease reads.
	MaxClockDrift float64
}

// Versions are a node's capabilities: the protocol range it speaks, the
// payload versions its state machine supports, and the committed cluster
// version.
type Versions struct {
	// ProtocolMin is the lowest protocol version it speaks.
	ProtocolMin uint32
	// ProtocolMax is the highest.
	ProtocolMax uint32
	// PayloadMax is the highest payload version it applies.
	PayloadMax uint32
	// Cluster is the committed cluster version.
	Cluster uint32
	// ClusterGeneration counts cluster-version raises.
	ClusterGeneration uint64
}
