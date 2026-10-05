// Package sql — range 0.2.24.* (ADR 0055 core/data/sql block), and the
// transaction manager's, health probe's and migration runner's 0.3.54.* (ADR
// 0055 service/data/sql block, declared here since ADR 0160).
//
// Package sql — declares the sentinel *errs.Error port outcomes, and the run
// outcomes of the transaction manager, the health probe and the migration
// runner in internal/service/data/sql (ADR 0160: every code is declared in the
// core, at the service's path). Each var's name equals its errs.Define Reason
// in SCREAMING_SNAKE form.
//
// # Public never carries infrastructure
//
// Driver errors are the SDK's worst leak risk: a failed connection reports the
// host, the port, the user and sometimes the password; a failed statement
// reports the statement. Every Public below is a fixed literal that names the
// CLASS of failure and nothing else, and no call site in this domain ever puts
// a DSN or a SQL fragment into one. Where the detail matters it goes in a
// Private or a field, both of which are log-only; and the run outcomes the
// service emits travel beside the driver's own error under errors.Join —
// reachable from a log, never from errs.PublicOf (ADR 0055 §D9).
//
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
//
// Package sql — hosts Dialect, the closed set of SQL engines this domain can
// spell, the vocabulary each one spells statements with, and the two refusals
// that keep the set closed.
//
// Package sql — hosts the four ports themselves. Kept apart from sql.go so
// the package's contract is one file: what a caller may implement, and what
// the SDK promises to accept.
//
// Package sql — hosts MigrationValue and the Step both of its halves are.
//
// Package sql — hosts TxOptionsValue, the per-transaction options a
// Transactor honours.
package sql
