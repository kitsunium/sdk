// Package net — shared sentinel-wrapping helper.
package net

import "github.com/kitsunium/sdk/internal/kernel/errs"

// wrapAs returns the given net sentinel as the error origin (its code, reason
// and public message win), attaching the cause's message and any extra fields as
// structured metadata. Wrapping the sentinel rather than the cause is what keeps
// the domain code intact: origin-wins would otherwise let an *errs.Error cause
// hijack it. A nil cause yields the sentinel carrying only the extra fields.
func wrapAs(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause contributes no field — keep only what the caller supplied.
	if cause == nil {
		//: wrap for the caller's fields alone.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: carry the cause message as a field so it stays diagnosable.
	return errs.Wrap(sentinel, errs.WrapParams{}, append(fields, errs.String("cause", cause.Error()))...)
}
