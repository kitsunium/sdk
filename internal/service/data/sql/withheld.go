package sql

import (
	"context"
	"errors"
	"fmt"
)

// newWithheld is NewWithheld's body: decl_gen.go writes NewWithheld, from the
// design, as one call of it.
func newWithheld(cause error) Withheld {
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

// unwrap is Withheld.Unwrap's body: decl_gen.go writes Withheld.Unwrap, from the
// design, as one call of it.
func (w Withheld) unwrap() error {
	//: the cause, as the driver returned it.
	return w.cause
}
