package sql

import (
	"context"
	"errors"
	"fmt"
)

// Withheld carries a driver's error for errors.Is and errors.As while keeping
// its text out of Error().
//
// A driver describes the row a statement touched. PostgreSQL's unique
// violation says "Key (doc_key)=(…) already exists" and MySQL's says
// "Duplicate entry '…'": a document store's key is routinely an e-mail
// address, and a queue's row holds the caller's payload. So a package whose
// rows hold a caller's data joins the driver's error through Withheld, beside
// a verdict of its own — errors.Join, never errs.Wrap, so the verdict's code
// stays the origin (CLAUDE.md rule 6) — and no rendering of the joined error
// quotes a row (ADR 0139 §D7, ADR 0151 §D6). The cause stays one errors.As
// away for a caller who knows its driver, and what it may log.
//
// Error() says only what names no row: a context's end, in the context's own
// words; otherwise the driver error's Go type, with its SQLSTATE or its result
// code when a method offers one — a class of failure, never its subject.
//
// The verdict stays each package's own — docstore's STATEMENT_FAILED, queue's
// QUEUE_BACKEND_FAILED — since each package's errors are its own; what they
// share is only this rendering of somebody else's.
type Withheld struct {
	// cause is the driver's own error.
	cause error
}

// NewWithheld returns cause withheld: reachable by errors.Is and errors.As,
// absent from Error().
func NewWithheld(cause error) Withheld {
	//: the cause, as the driver returned it.
	return Withheld{cause: cause}
}

// Error renders what can be said without the driver's words: a context's end
// in its own words, since those hold no data; otherwise the driver error's Go
// type, with its SQLSTATE or its result code when a method offers one.
func (w Withheld) Error() string {
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
func (w Withheld) Unwrap() error {
	//: the cause, as the driver returned it.
	return w.cause
}
