// Package sql_test — Withheld: a driver's error that errors.Is and errors.As
// still reach, and whose words no rendering repeats.
package sql_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// quotedRow is the datum every driver error below quotes, as PostgreSQL's
// "Key (doc_key)=(…) already exists" and MySQL's "Duplicate entry '…'" do.
const quotedRow string = "alice@example.com"

// driverError is a driver's error whose text quotes the row a statement
// touched, and which may carry the error a context ended with.
type driverError struct {
	// text is what the driver says, the row included.
	text string
	// cause is what the driver wraps, if anything.
	cause error
}

// Error renders the driver's words, row and all.
func (e *driverError) Error() string { return e.text }

// Unwrap reaches what the driver wrapped.
func (e *driverError) Unwrap() error { return e.cause }

// stateError is a driver error naming its SQLSTATE through a method, as pgx's
// PgError does.
type stateError struct {
	driverError
	// state is the SQLSTATE.
	state string
}

// SQLState names the class of failure.
func (e *stateError) SQLState() string { return e.state }

// codedError is a driver error naming its result code through a method, as
// modernc.org/sqlite's Error does.
type codedError struct {
	driverError
	// code is the result code.
	code int
}

// Code names the result code.
func (e *codedError) Code() int { return e.code }

// TestWithheldRendersNoneOfTheDriversWords pins what a joined driver error says
// in place of its text: a context's end in its own words, else the Go type with
// its SQLSTATE or its result code — and in no case the row. The texts are
// written out by hand, because the integration suites over real drivers compare
// against them.
func TestWithheldRendersNoneOfTheDriversWords(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cause error
		want  string
	}{
		{
			"a driver error with no method naming its class",
			&driverError{text: "duplicate key: Key (doc_key)=(" + quotedRow + ") already exists"},
			"*sql_test.driverError (the driver's text is withheld)",
		},
		{
			"a SQLSTATE, as pgx names it",
			&stateError{text: "Key (doc_key)=(" + quotedRow + ") already exists", state: "23505"},
			"*sql_test.stateError SQLSTATE 23505 (the driver's text is withheld)",
		},
		{
			"a result code, as modernc.org/sqlite names it",
			&codedError{text: "UNIQUE constraint failed: " + quotedRow, code: 2067},
			"*sql_test.codedError code 2067 (the driver's text is withheld)",
		},
		{
			"a deadline the driver wrapped",
			&driverError{text: "reading " + quotedRow, cause: context.DeadlineExceeded},
			"context deadline exceeded (the driver's text is withheld)",
		},
		{
			"a cancellation the driver wrapped",
			&driverError{text: "reading " + quotedRow, cause: context.Canceled},
			"context canceled (the driver's text is withheld)",
		},
	}
	for _, tc := range cases {
		withheld := svcsql.NewWithheld(tc.cause)
		if got := withheld.Error(); got != tc.want {
			t.Errorf("%s: Error() = %q, want %q", tc.name, got, tc.want)
		}
		joined := errors.Join(errs.Wrap(coresql.CommitFailed, errs.WrapParams{}), withheld)
		if strings.Contains(joined.Error(), quotedRow) {
			t.Errorf("%s: the joined error quotes the row: %s", tc.name, joined.Error())
		}
		if !errs.HasCode(joined, coresql.CodeCommitFailed) {
			t.Errorf("%s: the verdict's code is lost beside the driver's error: %v", tc.name, joined)
		}
		if !errors.Is(joined, tc.cause) {
			t.Errorf("%s: errors.Is no longer reaches the driver's error", tc.name)
		}
	}
}

// TestWithheldKeepsTheDriversErrorOneAsAway pins the other half of the
// contract: a caller who knows its driver still reaches the driver's own type,
// and a context's end still matches, through the withheld error and through
// the join a package returns.
func TestWithheldKeepsTheDriversErrorOneAsAway(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cause error
		check func(error) bool
	}{
		{
			"the driver's own type",
			&stateError{text: quotedRow, state: "40001"},
			func(err error) bool {
				stated, ok := errors.AsType[*stateError](err)
				return ok && stated.SQLState() == "40001"
			},
		},
		{
			"the context's deadline",
			&driverError{text: quotedRow, cause: context.DeadlineExceeded},
			func(err error) bool { return errors.Is(err, context.DeadlineExceeded) },
		},
	}
	for _, tc := range cases {
		withheld := svcsql.NewWithheld(tc.cause)
		if !errors.Is(withheld.Unwrap(), tc.cause) {
			t.Errorf("%s: Unwrap() does not hand back the driver's error", tc.name)
		}
		if !tc.check(withheld) {
			t.Errorf("%s: not reachable through Withheld", tc.name)
		}
		if !tc.check(errors.Join(errs.Wrap(coresql.CommitFailed, errs.WrapParams{}), withheld)) {
			t.Errorf("%s: not reachable through the join a package returns", tc.name)
		}
	}
}
