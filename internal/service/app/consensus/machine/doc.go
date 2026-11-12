// Package machine holds state machines for the consensus engine (ADR 0152
// D3): the glue every product wrote by hand around a database, and the
// reference key-value machine the linearizability checker models.
//
// [SQL] is a [coreconsensus.DurableStateMachine] and a
// [coreconsensus.BatchingStateMachine] over a [coresql.Transactor]. A
// batch of committed commands is applied in ONE transaction: the caller's
// [SQLConfig].Apply runs each command on the transaction's executor, and
// the machine writes the log identity and the position of the last one
// into its position row in that same transaction before it commits. A
// crash between the batch and the position, or during the commit, loses
// both or neither, so the engine resumes after exactly the entries the
// database holds: none applied twice, none skipped. A batch that cannot
// complete — the database's failure or Apply's — panics, and the node
// stops with STATE_MACHINE_FAILED: the entries are committed, and the
// restart applies them again from the position the database kept.
//
// A snapshot is read in a read-only transaction the machine opens on the
// apply goroutine (REPEATABLE READ, SERIALIZABLE on SQLite) and holds
// until Release, so [SQLConfig].Dump streams the state of the batch last
// committed while the next batches commit. A restore runs
// [SQLConfig].Truncate, [SQLConfig].Load and the snapshot's position in
// one transaction.
//
//	sm, err := machine.NewSQL(ctx, machine.SQLConfig{
//		Transactor: tm, Dialect: sql.DialectPostgres,
//		Apply:    applyCommand, // func(ctx, ex, entry) (result, error)
//		Dump:     dumpTables, Truncate: truncateTables, Load: loadTables,
//	})
//	node, err := consensus.NewNode(consensus.Config{Machine: sm, Durability: consensus.DurabilityDurable, …})
//
// [KV] is the reference machine: get, put and compare-and-swap on a
// per-key revision that only grows, so a value written back to an earlier
// one never passes a swap the caller read before (D6's ABA defence). Its
// commands are built with [KVGet], [KVPut] and [KVCompareAndSwap], and
// each answers a [KVResult]. It keeps its state in memory: a volatile
// machine, rebuilt from the latest snapshot and the log at every start.
package machine
