// Package consensus is the Raft engine of ADR 0152 and everything it ships
// around the step function of package raft: the event loop that drives it,
// the storage actor that makes a write durable before any reply that depends
// on it leaves, the apply goroutine, and snapshots cut, sent and installed in
// checksummed chunks.
//
// A [Node] is built by [NewRaft] over a [Config]: a state machine, a log, a
// snapshot store and a transport. The shipped storage is a [DataDir] — a
// lock, a stamp, the segmented [FileLog], the [FileSnapshots] and the stop
// markers — and the shipped transport is the mutual-TLS [TCPTransport];
// [MemoryLog], [MemorySnapshots] and [MemoryNetwork] stand in for them in
// tests.
//
// A failed write or sync stops the node with STORAGE_FAILED and is never
// retried (D4). The offline operations [Init], [Recover], [RestoreSnapshot],
// [Inspect] and [ClearQuarantine] work on a stopped node's directory only.
package consensus
