// Package consensus — ranges 0.2.57.* (ADR 0152, the domain's outcomes) and
// 0.3.92.* (ADR 0152, the shipped storage and transport's outcomes, declared
// here since ADR 0160).
//
// Package consensus declares the SDK's consensus DOMAIN: agreement of several
// machines on the order of their writes. It holds every value a port speaks —
// node and cluster identities, positions, entries, messages, snapshots,
// membership — and the frozen ports themselves: [Replicated], the consumer's
// algorithm-neutral port, [StateMachine] and its siblings, and the storage and
// transport ports [LogStore], [SnapshotStore] and [Transport], which speak
// Raft's terms (ADR 0152 D1).
//
// # A write is committed when a majority holds it on disk
//
// An entry is committed once a majority of the voters have made it durable.
// Every port in this package exists to keep that sentence true across crashes,
// partitions, restarts, recoveries and upgrades: [LogStore.Sync] and
// [LogStore.WriteStamp] carry the safety of the whole domain, and a failed
// sync stops the node rather than being retried.
//
// # This is not ADR 0120's state machine
//
// [StateMachine] is HOW a deterministic program is replicated: a pure
// transition applied in log order on every node. The statemachine domain
// decides WHAT state an entity is in and moves it on a timer. The two share a
// textbook name and nothing else.
//
// The engine, its storage and its transport live in
// internal/service/app/consensus; the facade is pkg/v1/app/consensus.
package consensus
