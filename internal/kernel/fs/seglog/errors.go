package seglog

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errSegmentTooLarge is a segment file larger than an int holds, which a
// 32-bit platform cannot read into one buffer.
const errSegmentTooLarge cause = "segment larger than an int holds"

// cause is the text of a failure's cause, recorded as a field of the error
// a caller matches on and never wrapped as an error of its own.
type cause string

// Error returns the cause's text unchanged.
func (c cause) Error() string { return string(c) }

// origin builds a fresh error on its own sentinel, with fields.
func origin(sentinel *errs.Error, fields ...errs.FieldValue) error {
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

// failed is the Failed of operation op, its cause kept as a field rather
// than wrapped, so the code a caller matches on is always Failed.
func failed(op string, c error) error {
	f := []errs.FieldValue{errs.String("op", op)}
	if c != nil {
		f = append(f, errs.String("cause", c.Error()))
	}
	return origin(Failed, f...)
}

// damaged is the Damaged of segment seg at offset off, for why.
func damaged(seg uint64, off int64, why string) error {
	return origin(Damaged, errs.String("segment", segName(seg)), errs.Int64("offset", off), errs.String("why", why))
}

// u64 renders v in decimal, the spelling of every index field.
func u64(v uint64) string { return strconv.FormatUint(v, 10) }

// outside is the error of a read of index on a log holding first through
// next-1: Dropped below, OutOfRange at or past the end.
func outside(index, first, next uint64) error {
	if index < first {
		return origin(Dropped, errs.String("index", u64(index)), errs.String("first", u64(first)))
	}
	return origin(OutOfRange, errs.String("index", u64(index)), errs.String("next", u64(next)))
}

// abandon is where an error with no reader goes: the cleanup of a file
// nothing will read again, whose outcome is already decided.
func abandon(err error) {
	if err == nil {
		return
	}
	// The resource is gone whatever the error says.
}
