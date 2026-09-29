// Package queue — the driver's error, carried beside the SQL broker's verdict
// and kept out of every rendering.
package queue

import (
	"context"
	"errors"
	"fmt"
)

// withheld carries a driver's error for errors.Is and errors.As while keeping
// its text out of Error().
//
// A driver describes the row a statement touched — PostgreSQL's constraint
// violations quote the key, MySQL's "Duplicate entry '…'" too — and a queue's
// row holds the caller's payload, which no error of this package quotes. The
// cause stays one errors.As away for a caller who knows its driver and what it
// may log. It is docstore's rule for its own SQL engine (ADR 0139 §D7), kept
// here rather than shared because each package's errors are its own.
type withheld struct {
	// cause is the driver's own error.
	cause error
}

// Error renders what can be said without the driver's words: a context's end
// in its own words, since those hold no data; otherwise the driver error's Go
// type, with its SQLSTATE or its result code when a method offers one.
func (w withheld) Error() string {
	//: a context's end is the caller's own, and quotes nothing.
	for _, ended := range []error{context.DeadlineExceeded, context.Canceled} {
		//: the deadline, or the cancellation.
		if errors.Is(w.cause, ended) {
			//: in the context's words.
			return ended.Error() + " (the driver's text is withheld)"
		}
	}
	//: pgx's PgError names its SQLSTATE through a method: a class of
	//: failure, which names no row.
	if stated, ok := errors.AsType[interface {
		error
		SQLState() string
	}](w.cause); ok {
		//: the class of failure.
		return fmt.Sprintf("%T SQLSTATE %s (the driver's text is withheld)", w.cause, stated.SQLState())
	}
	//: modernc.org/sqlite's Error names SQLite's result code through one.
	if coded, ok := errors.AsType[interface {
		error
		Code() int
	}](w.cause); ok {
		//: the result code.
		return fmt.Sprintf("%T code %d (the driver's text is withheld)", w.cause, coded.Code())
	}
	//: the kind of error and nothing of what it said.
	return fmt.Sprintf("%T (the driver's text is withheld)", w.cause)
}

// Unwrap hands errors.Is and errors.As the driver's own error.
func (w withheld) Unwrap() error {
	//: the cause, as the driver returned it.
	return w.cause
}
