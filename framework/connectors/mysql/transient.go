package mysql

import (
	"errors"

	driver "github.com/go-sql-driver/mysql"
)

// The MySQL errors that ask for the transaction to run again: a deadlock
// (ER_LOCK_DEADLOCK) and a lock wait past innodb_lock_wait_timeout
// (ER_LOCK_WAIT_TIMEOUT), which go-sql-driver exposes as a field, not a
// method, so the SDK's own reading cannot see them (#1000).
const (
	errLockDeadlock    uint16 = 1213
	errLockWaitTimeout uint16 = 1205
)

// Transient reports whether err is a deadlock or a lock wait timeout, which
// MySQL asks to run again.
func (engine) Transient(err error) bool {
	mysqlErr, ok := errors.AsType[*driver.MySQLError](err)
	return ok && (mysqlErr.Number == errLockDeadlock || mysqlErr.Number == errLockWaitTimeout)
}
