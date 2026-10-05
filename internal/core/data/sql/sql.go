package sql

import "context"

// TxFunc is the unit of work a [Transactor] runs inside a transaction.
//
// It is a FUNCTION port, the shape internal/core/CLAUDE.md already admits for
// resilience.Operation, scheduler.Job and lifecycle.Start: one behaviour, so a
// named func IS the contract. ADR 0039 is satisfied structurally — a func type
// cannot grow a method at all.
//
// ctx is the transaction-scoped context. Passing it on is what lets a callee
// join THIS transaction rather than open a second one, and what lets a nested
// [Transactor.Transact] become a savepoint instead of a deadlock against the
// transaction its own caller is holding.
//
// Returning nil commits (or releases the savepoint). Returning an error rolls
// back (or rolls back to the savepoint) and the error is returned to the
// caller VERBATIM, so errors.Is keeps working across the boundary.
//
// ex is valid ONLY for the duration of the call. Using it afterwards is
// refused with a typed error rather than left to database/sql's ErrTxDone.
type TxFunc func(ctx context.Context, ex Executor) error
