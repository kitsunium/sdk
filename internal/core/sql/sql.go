// Package sql declares the SDK's relational-database domain: a small set of
// PORTS above database/sql. It is deliberately NOT an ORM, and that refusal is
// the design, not an omission — see ADR 0055.
//
// # What this domain is
//
// Four things, and nothing else:
//
//   - [Executor] — the read/write surface a query runs against. *sql.DB,
//     *sql.Tx and *sql.Conn all satisfy it AS THEY ARE, with no adapter.
//   - [Transactor] — the transaction manager: it owns Begin, Commit and
//     Rollback, and it never hands any of the three to the code running
//     inside the transaction.
//   - [Checker] — a bounded liveness probe.
//   - [Migrator] — an ordered, versioned, mutually-exclusive schema runner.
//
// # What this domain refuses to be
//
// No entity mapping, no query builder, no lazy loading, no identity map, no
// change tracking, no repository generation, no schema reflection. A framework
// that needs those integrates a library that provides them; it does not grow
// them (ADR 0055 §D1). The SDK's job here is the part every application needs
// and nobody enjoys writing correctly: transaction ownership, savepoints,
// pool policy, and a migration runner that two processes can start at once.
//
// It also ships NO DRIVER. pgx, go-sql-driver/mysql and the sqlite bindings
// are connectors to a third-party system, so they belong under third-party/
// by the same rule that put the AWS writers there (ADR 0012). The consumer
// imports the driver it wants and hands this domain a *sql.DB.
//
// # The ports do not hide database/sql
//
// [Executor] speaks *sql.Rows, *sql.Row and sql.Result — the stdlib's own
// types, not re-declared equivalents. Re-declaring them would be the first
// step of the ORM this domain refuses to become: once Rows is ours, scanning
// is ours, and once scanning is ours, mapping is a small step. Organising the
// stdlib is the whole ambition.
//
// # There is no registry
//
// Like proc (ADR 0016), resilience (ADR 0026), net (ADR 0029), scheduler
// (ADR 0041), token (ADR 0042), session (ADR 0045) and lifecycle (ADR 0050),
// this domain has no name->implementation registry. There is one Transactor
// and one Migrator; a registry would have one entry and would add a way to
// select a database engine from a configuration string — which is precisely
// the mistake [Dialect] exists to prevent, since a dialect the SDK cannot
// spell must be refused at construction and not resolved at runtime.
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
