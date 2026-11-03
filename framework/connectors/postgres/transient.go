package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// The SQLSTATEs that ask for the transaction to run again (#1000).
const (
	stateSerializationFailure string = "40001"
	stateDeadlockDetected     string = "40P01"
)

// Transient reports whether err is a serialization failure or a detected
// deadlock, which PostgreSQL asks to run again.
func (engine) Transient(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (pgErr.Code == stateSerializationFailure || pgErr.Code == stateDeadlockDetected)
}
