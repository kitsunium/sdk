package consensus

// PositionValue is an entry's place in the log: the term it was created in and its
// index. The index is a fencing token: unique, strictly increasing over the
// group's life and identical on every node. The term says WHICH command, never
// who may act on it now (ADR 0152 D10).
type PositionValue struct {
	// Term is the leader term the entry was appended in.
	Term uint64
	// Index is the entry's place in the log, from 1.
	Index uint64
}
