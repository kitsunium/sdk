// Package clickhouse — declares the sentinels returned by the ClickHouse
// writer's constructor and INSERT path. Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form (short names per the AWS-writer convention; the
// package qualifier gives context). No DSN, credential, or record value is ever
// echoed into these errors (secret gate).
package clickhouse

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a database failure is an I/O problem.
const exitIOErr int = 74

var (
	// ClientInitFailed wraps a failure to build the sql handle: an invalid table
	// or an unresolvable credential provider.
	ClientInitFailed = errs.Define(CodeCHClientInitFailed, "CLIENT_INIT_FAILED",
		"ClickHouse writer could not initialise its client",
		"third-party/db/writer/clickhouse: invalid table or credentials unresolvable",
		errs.WithExitCode(exitIOErr))

	// InsertFailed wraps a failed batch INSERT.
	InsertFailed = errs.Define(CodeCHInsertFailed, "INSERT_FAILED",
		"ClickHouse writer failed to insert a log batch",
		"third-party/db/writer/clickhouse: the multi-row INSERT returned an error",
		errs.WithExitCode(exitIOErr))
)

// wrapClientInit wraps cause under the client-init sentinel. The credentials are
// never attached.
func wrapClientInit(cause error) error {
	//: single wrap point so every init failure carries the client-init code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeCHClientInitFailed,
		Reason:  "CLIENT_INIT_FAILED",
		Public:  "ClickHouse writer could not initialise its client",
		Private: "third-party/db/writer/clickhouse: client initialisation failed",
	})
}

// wrapInsert wraps cause under the insert sentinel with only the row count.
func wrapInsert(cause error, rows int) error {
	//: single wrap point so every INSERT failure carries the insert code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeCHInsertFailed,
		Reason:  "INSERT_FAILED",
		Public:  "ClickHouse writer failed to insert a log batch",
		Private: "third-party/db/writer/clickhouse: the multi-row INSERT returned an error",
	}, errs.Int("rows", rows))
}
