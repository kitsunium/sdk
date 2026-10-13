package consensus

import (
	"encoding/hex"
	"strconv"
)

// The Operator format: printable ASCII without the space, so an operator
// reads back from the audit file and the log exactly as it was typed, and a
// length bound that keeps it out of the way of the entry it is recorded in.
const (
	// operatorFirstByte is '!', the first printable ASCII byte after space.
	operatorFirstByte byte = 0x21
	// operatorLastByte is '~', the last printable ASCII byte.
	operatorLastByte byte = 0x7E
	// maxOperatorBytes bounds an operator's length.
	maxOperatorBytes int = 128
)

// NodeID names one member of a group for its whole life. It is a random 64-bit
// value drawn at Init, never 0 and never reused: a node whose disk was lost
// rejoins under a NEW id, because one that rejoined under its old id could vote
// twice in one term.
type NodeID uint64

// String renders the id in decimal, the form an operator types back.
func (id NodeID) String() string { return strconv.FormatUint(uint64(id), 10) }

// Incarnation is a random 128-bit value drawn when a data directory is first
// stamped. A wiped machine that kept its certificate and its NodeID is still
// a different incarnation, and is refused. It is an identifier, not a secret.
type Incarnation [16]byte

// String renders the incarnation in lowercase hex.
func (i Incarnation) String() string { return hex.EncodeToString(i[:]) }

// IsZero reports whether no incarnation was drawn.
func (i Incarnation) IsZero() bool { return i == Incarnation{} }

// ClusterID names one cluster: the founding, or a Recover or RestoreSnapshot
// that started a new history. It is 16 bytes everywhere it is encoded, the URI
// SAN included, where it is spelt in hex (see String).
type ClusterID [16]byte

// String renders the cluster id in lowercase hex, the form the certificate's
// URI SAN carries.
func (c ClusterID) String() string { return hex.EncodeToString(c[:]) }

// IsZero reports whether the cluster id was never set.
func (c ClusterID) IsZero() bool { return c == ClusterID{} }

// LogIdentity scopes positions: the ClusterID and a log epoch that Recover and
// RestoreSnapshot bump, and the predecessor this history descends from with the
// last index it kept. A position or a token from another identity is not
// comparable with this log's.
type LogIdentity struct {
	// Cluster is the cluster the log belongs to.
	Cluster ClusterID
	// Epoch counts the histories of this cluster id: 1 at founding.
	Epoch uint64
	// Predecessor is the cluster of the history this one was recovered from,
	// zero for a founding.
	Predecessor ClusterID
	// PredecessorEpoch is the predecessor's epoch.
	PredecessorEpoch uint64
	// KeptIndex is the last index Recover kept from the predecessor. An
	// applied position of the predecessor at or below it is still trusted.
	KeptIndex uint64
}

// Same reports whether two identities name the same history (cluster and
// epoch), whatever lineage they record.
func (l LogIdentity) Same(o LogIdentity) bool {
	return l.Cluster == o.Cluster && l.Epoch == o.Epoch
}

// Token is a lineage-scoped position: the cluster, the log epoch, the term and
// the index. It is comparable, and its encoding is fixed at 41 bytes: a version
// byte, the 16-byte ClusterID, then epoch, term and index as big-endian uint64
// (see AppendToken in the service's wire package).
type Token struct {
	// Cluster is the cluster of the log that committed the entry.
	Cluster ClusterID
	// Epoch is that log's epoch.
	Epoch uint64
	// Term is the entry's term.
	Term uint64
	// Index is the entry's index.
	Index uint64
}

// Position returns the token's position, without its lineage.
func (t Token) Position() Position { return Position{Term: t.Term, Index: t.Index} }

// Operator is the opaque identity an administrative operation is performed
// under. The product vouches for it and the SDK records it in the entry,
// never authenticating it. It is printable ASCII 0x21-0x7E, at most 128 bytes,
// and must not carry personal data: the log is replicated and kept.
type Operator string

// Valid reports whether the operator satisfies the format rules.
func (o Operator) Valid() bool {
	if len(o) == 0 || len(o) > maxOperatorBytes {
		return false
	}
	for i := range len(o) {
		if o[i] < operatorFirstByte || o[i] > operatorLastByte {
			return false
		}
	}
	return true
}

// AlarmID names an alarm by the index of the entry that raised it, so that a
// clearing operation names the alarm it means to clear.
type AlarmID uint64

// Precondition is what a clearing or raising operation expects to act on: the
// log identity it was read in, and the alarm or the contract or cluster-version
// generation it expects to replace. Apply refuses a mismatch with
// ADMIN_PRECONDITION_FAILED, so a stale page cannot clear an alarm raised since.
type Precondition struct {
	// Log is the identity the precondition was read in.
	Log LogIdentity
	// Alarm is the alarm a clearing operation names.
	Alarm AlarmID
	// Generation is the contract or cluster-version generation an operation
	// expects to replace.
	Generation uint64
}
