package consensus

// StampState says how far a data directory has come.
type StampState uint8

// The states of a stamp.
const (
	// StampJoining is a directory Init prepared: it may join as an empty
	// learner, never vote or acknowledge until it is admitted.
	StampJoining StampState = iota + 1
	// StampBootstrapPending is a founding in progress: Bootstrap wrote it
	// before it wrote the founding configuration.
	StampBootstrapPending
	// StampMember is a directory that belongs to a cluster.
	StampMember
)

// Stamp binds a data directory to one node of one cluster: written by
// LogStore.WriteStamp, durable on return, and verified at every start. A
// mismatch is DATA_DIR_MISMATCH.
type Stamp struct {
	// Log is the log identity the directory belongs to.
	Log LogIdentity
	// ID is the node.
	ID NodeID
	// Incarnation is drawn when the stamp is first written.
	Incarnation Incarnation
	// Slot is the placement slot the node's certificate names.
	Slot string
	// State is how far the directory has come.
	State StampState
	// Founding is the digest of the founding member list while a bootstrap
	// is pending, so that a rerun with another list is refused.
	Founding [32]byte
}

// JoinConfig is what a joining node needs to send its JOIN: where to reach the
// members, and the stamp Init wrote, whose id and incarnation the member it
// reaches proposes as a learner.
type JoinConfig struct {
	// Addresses are the members to try, in order.
	Addresses []string
	// Stamp is the joiner's stamp, from Init.
	Stamp Stamp
	// Address is where the joiner listens.
	Address string
	// Pins are the SPKIs of the members the joiner trusts, from the
	// operator. An answer counts only from a peer presenting one of them,
	// and only when the membership it returns lists that peer under that
	// key; an empty list refuses the JOIN.
	Pins [][32]byte
}
