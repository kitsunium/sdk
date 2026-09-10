// Package sql — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// # Public never carries infrastructure
//
// Driver errors are the SDK's worst leak risk: a failed connection reports the
// host, the port, the user and sometimes the password; a failed statement
// reports the statement. Every Public below is a fixed literal that names the
// CLASS of failure and nothing else, and no call site in this domain ever puts
// a DSN or a SQL fragment into one. Where the detail matters it goes in a
// Private or a field, both of which are log-only (ADR 0055 §D9).
package sql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused dialect and a refused
// migration are permanent wiring faults: the same call will be refused
// forever, and the fix is a code change, never a retry.
const exitConfig int = 78

var (
	// UnknownDialect is returned by ParseDialect for a name that is in
	// neither the accepted nor the refused set.
	UnknownDialect = errs.Define(CodeUnknownDialect, "UNKNOWN_DIALECT",
		"The SQL dialect is not one this SDK supports",
		"core/sql: dialect name is not in the accepted set (postgres, mysql, sqlite); the field carries the name",
		errs.WithExitCode(exitConfig))

	// DialectRefused is returned by ParseDialect for an engine this SDK
	// recognises and declines. It is a DIFFERENT answer from UnknownDialect:
	// a caller debugging a refusal needs to know whether their engine is
	// unheard-of or known-and-declined, because only the second one comes
	// with a reason they can evaluate.
	DialectRefused = errs.Define(CodeDialectRefused, "DIALECT_REFUSED",
		"That SQL engine is recognised and deliberately not supported",
		"core/sql: the engine's savepoint grammar is a different algorithm, not a different string; the reason field says which",
		errs.WithExitCode(exitConfig))

	// NestedIsolation is returned when a nested Transact asks for non-zero
	// options. A nested scope is a SAVEPOINT inside an already-open
	// transaction, and a savepoint changes neither the isolation level nor
	// the read-only-ness of that transaction. Honouring the request is
	// impossible; ignoring it would hand the caller a weaker transaction than
	// the one they asked for, under a nil error.
	NestedIsolation = errs.Define(CodeNestedIsolation, "NESTED_ISOLATION",
		"A nested transaction cannot change isolation or read-only mode",
		"core/sql: a nested scope is a savepoint; open the outer transaction with the options instead",
		errs.WithExitCode(exitConfig))

	// InvalidMigration is returned for a migration that could never run. The
	// "missing" field names which half is absent.
	InvalidMigration = errs.Define(CodeInvalidMigration, "INVALID_MIGRATION",
		"The migration is not runnable and was refused",
		"core/sql: migration has version 0, an empty name, a nil Up or a nil Down; the missing field names which",
		errs.WithExitCode(exitConfig))

	// MigrationIrreversible is what the Irreversible step returns. It is the
	// caller's own declaration coming back at them, not a defect the SDK
	// found — which is why it reads as a statement about the migration rather
	// than about the runner.
	MigrationIrreversible = errs.Define(CodeMigrationIrreversible, "MIGRATION_IRREVERSIBLE",
		"That migration was declared irreversible and cannot be rolled back",
		"core/sql: Down is the Irreversible step; reversing it requires a new forward migration",
		errs.WithExitCode(exitConfig))
)
