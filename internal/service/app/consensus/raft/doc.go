// Package raft is the consensus engine's step function (ADR 0152 D15): a
// value with no goroutine, no I/O and no clock. Every decision of the
// protocol is taken here — PreVote, elections and the election restriction,
// CheckQuorum, the vote refusals, the leader lease and ReadIndex, log
// replication and commit, leadership transfer, single-server membership
// changes through learners, the snapshot offer — and every effect leaves
// through [Raft.Ready] for a driver to perform.
//
// Instants come in with each input, taken by the driver from its injected
// clock, and are compared with Sub only, never rounded, so the lease keeps
// Go's monotonic reading (D9, R7-20). Randomness comes from a seed in
// [Config], so a run is a pure function of its inputs (D15).
//
// Durability is an input, never an assumption (D5b): a write the step
// function asks for is counted only once the driver reports it with
// [Raft.StorageDone], and the replies that depend on it leave in
// [Ready.AfterSync], which the driver sends only once that write is durable.
package raft
