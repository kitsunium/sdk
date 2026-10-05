package sql

import (
	"errors"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// failed returns the SDK's verdict for a driver-level failure, side by side
// with the driver's own error.
//
// errors.Join rather than errs.Wrap, for the reason ADR 0050 gives: origin-wins
// (CLAUDE.md rule 6) would make the driver's error the origin and relabel the
// SDK's verdict with whatever code the driver happened to carry. Side by side,
// errs.HasCode(err, CodeCommitFailed) and the caller's own
// errors.Is(err, driver.ErrBadConn) both answer.
//
// The driver's error is never read into a Public: the verdict's own Public is
// a fixed literal, and the join only affects Error() and errors.Is.
func failed(sentinel *kerrs.Error, cause error, fields ...kerrs.FieldValue) error {
	//: the SDK's verdict carries the code, the reason and the wire-safe text.
	verdict := kerrs.Wrap(sentinel, kerrs.WrapParams{}, fields...)
	//: a refusal with no driver behind it stands alone.
	if cause == nil {
		//: one error, one code.
		return verdict
	}
	//: both, so neither question loses its answer.
	return errors.Join(verdict, cause)
}
