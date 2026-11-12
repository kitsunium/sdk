// Package machine is the public facade of the consensus domain's state
// machines (ADR 0152 D3): the durable machine over a database every
// product otherwise writes by hand, and the reference key-value machine.
//
// [NewSQL] builds a [SQL] machine over a transactor of package
// [github.com/kitsunium/sdk/pkg/v1/data/sql]. It applies a batch of
// committed commands in one transaction and writes the applied log identity
// and position in that same transaction, so a crash between the two loses
// both or neither and a node configured with DurabilityDurable resumes after
// exactly the entries the database holds: none applied twice, none skipped.
// A batch that cannot complete panics, and the node stops with
// STATE_MACHINE_FAILED. A snapshot streams from a read-only transaction
// opened when it is taken; a restore truncates and loads in one transaction
// with the snapshot's position.
//
//	sm, err := machine.NewSQL(ctx, machine.SQLConfig{
//		Transactor: tm, Dialect: sql.DialectPostgres,
//		Apply:    applyCommand, // func(ctx, ex, entry) (result, error)
//		Dump:     dumpTables, Truncate: truncateTables, Load: loadTables,
//	})
//	node, err := consensus.NewNode(consensus.Config{Machine: sm, Durability: consensus.DurabilityDurable, …})
//
// [KV] keeps keys in memory, each with a revision that only grows: [KVPut]
// stores a value, [KVGet] reads it through the log, [KVCompareAndSwap]
// stores one only while the revision is the one the caller read, so a value
// written back to an earlier one never passes a stale swap. Each command
// answers a [KVResult] from Propose.
//
//	_, res, err := node.Propose(ctx, machine.KVCompareAndSwap("leader", []byte("fra"), 0))
//	if r, ok := res.(machine.KVResult); ok && r.Applied { … } // took the key at revision r.Revision
package machine
