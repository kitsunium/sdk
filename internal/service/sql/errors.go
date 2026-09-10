// Package sql — declares the sentinel *errs.Error run outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// # No Public in this file describes infrastructure
//
// A driver error is the most dangerous cause an SDK can wrap: a failed dial
// names the host, the port and often the user; a failed statement names the
// statement. Every Public below is a fixed literal naming the CLASS of
// failure, and the driver's own error is attached with errors.Join — reachable
// from a log, never from errs.PublicOf. No call site in this package puts a
// DSN, a query, or a bound argument into a Public (ADR 0055 §D9).
package sql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78): a permanent wiring fault, fixed
// by a code change and never by a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75): a timing or availability
// outcome, where the same call on a less loaded system may well succeed.
const exitTempFail int = 75

// httpUnavailable is the wire status for "the database did not answer". It is
// 503 rather than the default 500 because the caller's request was never the
// problem, and a load balancer reads the difference.
const httpUnavailable int = 503

var (
	// ConfigInvalid is returned by every constructor in this package for a
	// Config that could never produce a working port.
	ConfigInvalid = errs.Define(CodeConfigInvalid, "CONFIG_INVALID",
		"The SQL configuration is incomplete and was refused",
		"service/sql: Config has a nil DB or an unusable Dialect; the missing field names which",
		errs.WithExitCode(exitConfig))

	// PoolMisconfigured is returned when a pool field's zero value would be
	// read by database/sql as a policy nobody chose. MaxOpen is the case that
	// matters: database/sql reads 0 as UNLIMITED, and "as many connections as
	// the application happens to want" is a decision made by forgetting a
	// line, not by taking one (ADR 0031's refusal side).
	PoolMisconfigured = errs.Define(CodePoolMisconfigured, "POOL_MISCONFIGURED",
		"The connection-pool policy is not one this SDK will apply",
		"service/sql: a non-positive MaxOpen means unlimited in database/sql; state the ceiling explicitly",
		errs.WithExitCode(exitConfig))

	// BeginFailed is joined with the driver's error when a transaction could
	// not be opened. Nothing ran.
	BeginFailed = errs.Define(CodeBeginFailed, "BEGIN_FAILED",
		"The database refused to open a transaction",
		"service/sql: BeginTx failed; the driver error travels beside this one under errors.Join",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// CommitFailed is joined with the driver's error when COMMIT was
	// rejected. It says the unit of work did NOT take effect, which is the
	// one fact a caller must not have to infer.
	CommitFailed = errs.Define(CodeCommitFailed, "COMMIT_FAILED",
		"The transaction could not be committed and had no effect",
		"service/sql: COMMIT was rejected; the driver error travels beside this one under errors.Join",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// RollbackFailed travels ALONGSIDE the error that triggered the rollback,
	// never instead of it. A teardown that also breaks is a second defect,
	// and reporting only it would hide the first — the rule ADR 0050 wrote
	// for UnwindFailed, applied to the same shape one layer down.
	RollbackFailed = errs.Define(CodeRollbackFailed, "ROLLBACK_FAILED",
		"Rolling the transaction back did not complete cleanly",
		"service/sql: ROLLBACK was rejected; the connection is discarded by database/sql",
		errs.WithExitCode(exitTempFail))

	// SavepointFailed is returned when SAVEPOINT, RELEASE SAVEPOINT or
	// ROLLBACK TO SAVEPOINT is rejected. The "statement" field names which of
	// the three, because the consequences differ: a failed SAVEPOINT means
	// the nested scope never started, while a failed ROLLBACK TO means the
	// engine's state is unknown and the transaction is poisoned.
	SavepointFailed = errs.Define(CodeSavepointFailed, "SAVEPOINT_FAILED",
		"A savepoint statement was rejected by the database",
		"service/sql: the statement field names which of SAVEPOINT / RELEASE / ROLLBACK TO failed",
		errs.WithExitCode(exitTempFail))

	// TxPoisoned refuses every further operation on a transaction whose
	// savepoint rollback failed.
	//
	// This is the answer to "does a failed sub-transaction condemn the outer
	// one?". Normally NO: ROLLBACK TO SAVEPOINT is precisely the statement
	// that clears PostgreSQL's aborted state, so a caught inner failure
	// leaves a usable transaction. But when that statement ITSELF fails, the
	// engine's state is no longer known, and continuing would commit a
	// transaction containing work the SDK believes it undid.
	TxPoisoned = errs.Define(CodeTxPoisoned, "TX_POISONED",
		"The transaction is no longer usable and will not be committed",
		"service/sql: a savepoint rollback failed, so the engine state is unknown; the whole transaction is abandoned",
		errs.WithExitCode(exitTempFail))

	// TxClosed is returned when an Executor is used after the scope that lent
	// it returned. database/sql would answer ErrTxDone for a finished
	// transaction and would EXECUTE for a nested scope whose outer
	// transaction is still open — the second case is the one worth catching.
	TxClosed = errs.Define(CodeTxClosed, "TX_CLOSED",
		"That transaction handle is no longer valid",
		"service/sql: the Executor outlived its Transact scope; it is valid only for the duration of the call",
		errs.WithExitCode(exitConfig))

	// HealthCheckFailed is joined with the driver's error when the probe was
	// answered with a failure.
	HealthCheckFailed = errs.Define(CodeHealthCheckFailed, "HEALTH_CHECK_FAILED",
		"The database is not reachable",
		"service/sql: PingContext returned an error; the driver error travels beside this one under errors.Join",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// HealthCheckTimeout is returned when the probe did not answer inside its
	// budget. It is a separate code from HealthCheckFailed because a database
	// that refuses is a different operational fact from one that hangs, and a
	// readiness endpoint routes on the difference.
	HealthCheckTimeout = errs.Define(CodeHealthCheckTimeout, "HEALTH_CHECK_TIMEOUT",
		"The database did not answer the health check in time",
		"service/sql: the probe budget expired; the ping goroutine is abandoned, never killed",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// MigrationFailed is joined with the step's own error when a migration or
	// its bookkeeping row fails. The transaction was rolled back, so neither
	// the schema change nor the version row took effect — except on engines
	// with non-transactional DDL, which is stated in the package doc rather
	// than papered over.
	MigrationFailed = errs.Define(CodeMigrationFailed, "MIGRATION_FAILED",
		"A migration did not apply and was rolled back",
		"service/sql: version+direction fields; step error joined; on MySQL DDL commits implicitly, "+
			"so a failed step can leave the schema change applied WITHOUT its version row (ADR 0055 D8)",
		errs.WithExitCode(exitTempFail))

	// MigrationOutOfOrder refuses a pending migration whose version is lower
	// than one already applied. It is the two-branches-merged hazard: both
	// branches numbered from the same base, one shipped first, and the other
	// would now apply UNDER it — producing a schema that matches neither
	// branch's expectation and a version table that cannot express what
	// happened.
	MigrationOutOfOrder = errs.Define(CodeMigrationOutOfOrder, "MIGRATION_OUT_OF_ORDER",
		"A pending migration is older than one already applied",
		"service/sql: renumber the migration above the highest applied version; the fields carry both",
		errs.WithExitCode(exitConfig))

	// MigrationLockUnsupported refuses a Migrator at CONSTRUCTION when the
	// dialect has no session-scoped advisory lock. Refusing here rather than
	// running unlocked is the whole point: a runner that silently drops
	// mutual exclusion is at its most dangerous exactly when two instances
	// start together, which is the case it exists for.
	MigrationLockUnsupported = errs.Define(CodeMigrationLockUnsupported, "MIGRATION_LOCK_UNSUPPORTED",
		"That dialect has no advisory lock, so migrations cannot be serialised",
		"service/sql: only postgres and mysql expose a session-scoped advisory lock; wire your own exclusion",
		errs.WithExitCode(exitConfig))

	// MigrationLockTimeout is returned when another holder kept the migration
	// lock for the whole budget. Nothing was applied.
	MigrationLockTimeout = errs.Define(CodeMigrationLockTimeout, "MIGRATION_LOCK_TIMEOUT",
		"Another process is running migrations and did not finish in time",
		"service/sql: the advisory lock was held throughout the budget; no migration was applied",
		errs.WithExitCode(exitTempFail))

	// MigrationUnknownVersion refuses a Down over a version the running
	// binary has no migration for. The database is ahead of the code, and
	// reversing a change whose Down is not present would be guessing.
	MigrationUnknownVersion = errs.Define(CodeMigrationUnknownVersion, "MIGRATION_UNKNOWN_VERSION",
		"The database records a migration this build does not carry",
		"service/sql: Down cannot reverse a version with no Step; deploy the build that owns it",
		errs.WithExitCode(exitConfig))

	// VersionTableInvalid refuses a version-table name that is not a bare SQL
	// identifier. A table name cannot be a bound parameter, so it is
	// interpolated — which makes validating it the only thing between a
	// configuration string and an injection.
	VersionTableInvalid = errs.Define(CodeVersionTableInvalid, "VERSION_TABLE_INVALID",
		"The version-table name is not a plain SQL identifier",
		"service/sql: an identifier cannot be bound as a parameter, so only [A-Za-z_][A-Za-z0-9_]* is accepted",
		errs.WithExitCode(exitConfig))

	// DuplicateMigration refuses two migrations declaring the same version.
	// The version IS the identity, so a duplicate makes "has this been
	// applied?" unanswerable.
	DuplicateMigration = errs.Define(CodeDuplicateMigration, "DUPLICATE_MIGRATION",
		"Two migrations declare the same version",
		"service/sql: a version identifies a migration; the field carries the duplicated value",
		errs.WithExitCode(exitConfig))
)
