package consensus

import "slices"

// MemberValue is one node of the membership: its identity, the key it is pinned
// to, where it listens, and whether it votes.
type MemberValue struct {
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

// MembershipValue is the configuration at a position: its members and the set of
// retired IDs, which is never reused.
type MembershipValue struct {
	// Index is the index of the entry that produced it, 0 at founding.
	Index uint64
	// Members lists every voter and learner.
	Members []MemberValue
	// Retired lists every removed NodeID.
	Retired []NodeID
}

// Member returns the member with that id.
func (m MembershipValue) Member(id NodeID) (MemberValue, bool) {
	for _, x := range m.Members {
		if x.ID == id {
			return x, true
		}
	}
	return MemberValue{}, false
}

// IsRetired reports whether the id was removed.
func (m MembershipValue) IsRetired(id NodeID) bool {
	return slices.Contains(m.Retired, id)
}

// Voters returns the ids of the voters, in membership order.
func (m MembershipValue) Voters() []NodeID {
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
func (m MembershipValue) Clone() MembershipValue {
	return MembershipValue{
		Index: m.Index,
		// append(s[:0:0], s...) is the copy slices.Clone makes, nil kept nil,
		// written out because slices.Clone's own body would push the engine's
		// ConfAt, which inlines this method, past the inliner's budget.
		Members: append(m.Members[:0:0], m.Members...),
		Retired: append(m.Retired[:0:0], m.Retired...),
	}
}
