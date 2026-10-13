// Package consensus is the public facade of the SDK's consensus domain
// (ADR 0152): agreement of several machines on the order of their writes.
// A write is committed when a majority holds it on disk.
//
//	id, inc, _ := consensus.Init(dir, cluster, "fra")      // offline, every founder
//	_ = consensus.Bootstrap(ctx, cfg, members)              // offline, identical list
//	node, _ := consensus.NewRaft(cfg)                       // never bootstraps
//	tok, result, err := node.Propose(ctx, command)
//
// # After OUTCOME_UNKNOWN, never retry blindly
//
// NOT_LEADER means the proposal was certainly NOT appended and is safe to
// retry. OUTCOME_UNKNOWN means it was appended, or possibly delivered, and the
// answer was lost: it may still commit. Read the state through Barrier and
// decide, or carry a per-key revision in the command, because a replayed
// compare-and-swap is not harmless under ABA (D6, R1-04).
//
// # Reads
//
// Barrier returns once the local state machine reflects every write
// committed before the call: ReadIndex by default, confirmed by a heartbeat
// round started after the read arrived. ReadLease is opt-in and sound only
// under the four conditions of D9; a leader paused after its lease check
// serves a stale read, which ReadIndex never does.
//
// # Apply is a pure transition
//
// StateMachine.Apply is called for every committed command, in log order, on
// one goroutine, and must be deterministic. An Apply that cannot complete
// must panic: the node stops with STATE_MACHINE_FAILED, and the entry IS
// committed.
package consensus
