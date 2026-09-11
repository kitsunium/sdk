// Package sql — range 0.3.54.* (ADR 0055 service/sql block).
package sql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.54.0 - 0.3.54.255

// CodeConfigInvalid identifies a constructor refused because the Config could
// never produce a working port: a nil *sql.DB or an unusable Dialect.
const CodeConfigInvalid errs.Code = 0x00_03_36_01 // 0.3.54.1

// CodePoolMisconfigured identifies a pool policy the SDK refuses to apply
// because the value database/sql would read is not the one the caller meant.
const CodePoolMisconfigured errs.Code = 0x00_03_36_02 // 0.3.54.2

// CodeBeginFailed identifies a transaction the driver would not open.
const CodeBeginFailed errs.Code = 0x00_03_36_03 // 0.3.54.3

// CodeCommitFailed identifies a transaction whose COMMIT was rejected. The
// unit of work did NOT take effect.
const CodeCommitFailed errs.Code = 0x00_03_36_04 // 0.3.54.4

// CodeRollbackFailed identifies a ROLLBACK the driver refused. It travels
// alongside the error that triggered the rollback, never instead of it.
const CodeRollbackFailed errs.Code = 0x00_03_36_05 // 0.3.54.5

// CodeSavepointFailed identifies a SAVEPOINT, RELEASE SAVEPOINT or ROLLBACK
// TO SAVEPOINT statement the engine rejected.
const CodeSavepointFailed errs.Code = 0x00_03_36_06 // 0.3.54.6

// CodeTxPoisoned identifies an operation refused because the transaction can
// no longer be trusted: its savepoint rollback failed, so the engine's state
// is unknown and nothing more may be committed on it.
const CodeTxPoisoned errs.Code = 0x00_03_36_07 // 0.3.54.7

// CodeTxClosed identifies an Executor used after the scope that lent it
// returned.
const CodeTxClosed errs.Code = 0x00_03_36_08 // 0.3.54.8

// CodeHealthCheckFailed identifies a liveness probe the database answered
// with an error.
const CodeHealthCheckFailed errs.Code = 0x00_03_36_09 // 0.3.54.9

// CodeHealthCheckTimeout identifies a liveness probe that did not answer
// within its budget.
const CodeHealthCheckTimeout errs.Code = 0x00_03_36_0A // 0.3.54.10

// CodeMigrationFailed identifies a migration step, or its version-table
// bookkeeping, that failed. The transaction was rolled back.
const CodeMigrationFailed errs.Code = 0x00_03_36_0B // 0.3.54.11

// CodeMigrationOutOfOrder identifies a pending migration whose version is
// lower than one already applied — the two-branches-merged hazard.
const CodeMigrationOutOfOrder errs.Code = 0x00_03_36_0C // 0.3.54.12

// CodeMigrationLockUnsupported identifies a Migrator refused at construction
// because the dialect has no session-scoped advisory lock.
const CodeMigrationLockUnsupported errs.Code = 0x00_03_36_0D // 0.3.54.13

// CodeMigrationLockTimeout identifies a migration run abandoned because
// another holder kept the lock for the whole budget.
const CodeMigrationLockTimeout errs.Code = 0x00_03_36_0E // 0.3.54.14

// CodeMigrationUnknownVersion identifies a version recorded in the database
// that this binary carries no migration for — so it cannot be reversed.
const CodeMigrationUnknownVersion errs.Code = 0x00_03_36_0F // 0.3.54.15

// CodeVersionTableInvalid identifies a version-table name that cannot be
// interpolated into SQL safely. Identifiers cannot be bound as parameters.
const CodeVersionTableInvalid errs.Code = 0x00_03_36_10 // 0.3.54.16

// CodeDuplicateMigration identifies two migrations declaring the same
// version.
const CodeDuplicateMigration errs.Code = 0x00_03_36_11 // 0.3.54.17
