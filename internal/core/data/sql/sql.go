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

// Transient is the verdict on a failed transaction: true when the database is
// saying "run it again" — a serialization failure, a deadlock, a lock another
// writer held past its timeout — so the whole unit of work may run again from
// its BEGIN; false for everything else.
//
// A FUNCTION port, for the reason [TxFunc] is one: one behaviour, so a named
// func IS the contract (ADR 0039). The SDK ships no driver, so it reads a
// driver's error only through the methods drivers expose, and
// internal/service/data/sql's Classify is that reading; a connector that
// knows its driver's error type writes its own and composes the two.
//
// A context's end is never transient: the caller stopped waiting, and running
// the work again would outlive the caller who asked for it.
type Transient func(err error) bool
