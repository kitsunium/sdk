package sqlite

import (
	"errors"
)

// SQLite's primary result codes that ask for the transaction to run again:
// BUSY and LOCKED, in every extended form — SQLITE_BUSY_SNAPSHOT (517), the
// write a WAL snapshot can no longer make, among them —, the low byte of the
// code modernc.org/sqlite's error names through Code (#1000).
const (
	resultBusy    int = 5
	resultLocked  int = 6
	resultPrimary int = 0xff
)

// Transient reports whether err is SQLite's busy or locked, which asks to run
// the transaction again: past the busy timeout, another writer held the file.
func (engine) Transient(err error) bool {
	coded, ok := errors.AsType[interface {
		error
		Code() int
	}](err)
	if !ok {
		return false
	}
	primary := coded.Code() & resultPrimary
	return primary == resultBusy || primary == resultLocked
}
