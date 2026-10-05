package mysql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a database failure is an I/O problem.
const exitIOErr int = 74

// wrapClientInit wraps cause under the client-init sentinel. The DSN is never
// attached (it embeds the password).
func wrapClientInit(cause error) error {
	//: single wrap point so every init failure carries the client-init code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeMySQLClientInitFailed,
		Reason:  "CLIENT_INIT_FAILED",
		Public:  "MySQL writer could not initialise its client",
		Private: "third-party/db/writer/mysql: client initialisation failed",
	})
}

// wrapInsert wraps cause under the insert sentinel with only the row count.
func wrapInsert(cause error, rows int) error {
	//: single wrap point so every INSERT failure carries the insert code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeMySQLInsertFailed,
		Reason:  "INSERT_FAILED",
		Public:  "MySQL writer failed to insert a log batch",
		Private: "third-party/db/writer/mysql: the multi-row INSERT returned an error",
	}, errs.Int("rows", rows))
}
