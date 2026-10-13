package consensus

// EntryKind says what a log entry carries. Only [EntryCommand] reaches the
// state machine; every other kind is the engine's own replicated state. A kind
// a node does not know stops it with VERSION_UNSUPPORTED: it is never skipped.
type EntryKind uint8

// The entry kinds of protocol version 1. The range 0x80-0x8F is reserved for
// ADR 0173 and refused until the cluster version enables it.
const (
	// EntryCommand is a caller's command, handed to StateMachine.Apply.
	EntryCommand EntryKind = iota + 1
	// EntryNoOp is the empty entry a new leader commits in its term before it
	// serves a read or accepts a membership change.
	EntryNoOp
	// EntryMembership carries a whole new Membership and the change that
	// produced it. It takes effect when APPENDED.
	EntryMembership
	// EntryTimingContract carries a new committed timing contract.
	EntryTimingContract
	// EntryClusterVersion raises the protocol version every member must speak.
	EntryClusterVersion
	// EntryNoSpaceRaise raises the replicated no-space alarm.
	EntryNoSpaceRaise
	// EntryNoSpaceClear clears the no-space alarm it names.
	EntryNoSpaceClear
	// EntryTransfer records a leadership transfer and its operator.
	EntryTransfer
)

// Known reports whether the kind is one protocol version 1 defines.
func (k EntryKind) Known() bool { return k >= EntryCommand && k <= EntryTransfer }

// Entry is one record of the replicated log. Data is immutable once appended:
// the store and the engine never write into it, and a caller that hands bytes
// to Propose gives up their ownership.
type Entry struct {
	// Position is the entry's term and index.
	Position Position
	// Kind says what Data carries.
	Kind EntryKind
	// Version is the payload version the proposer set; it selects the frozen
	// code path that applies the entry.
	Version uint32
	// BodySum is the CRC-32C of Data, computed by the proposer.
	BodySum uint32
	// ChainSum chains the previous entry's header: computed by the LEADER at
	// its serialized append point, verified at append and before Apply.
	ChainSum uint32
	// Data is the payload: a command for EntryCommand, the engine's encoding
	// for the other kinds.
	Data []byte
	// Token is the entry's lineage-scoped command identity, set in memory by
	// the engine before Apply and never encoded in the log.
	Token Token
}

// HardState is what a node must never forget: its term, its vote in that term
// and the commit index it knows of. Term and vote are durable before any reply
// depends on them; the commit index is written lazily, never above the last
// synced index.
type HardState struct {
	// Term is the node's current term.
	Term uint64
	// Vote is the candidate voted for in Term, 0 for none.
	Vote NodeID
	// Commit is the highest index known committed.
	Commit uint64
}
