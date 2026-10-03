// Package mail — the one helper through which this package raises a sentinel
// over a cause. The domain's codes and sentinels — the message refusals, and
// the outcomes of composition and of the SMTP session — are declared in
// internal/core/app/mail (ADR 0160).
package mail

import "github.com/kitsunium/sdk/internal/kernel/errs"

// wrapAs returns the given mail sentinel as the error origin — its code,
// reason and public message win — attaching the cause's message and any extra
// fields as structured metadata.
//
// Wrapping the sentinel rather than the cause is what keeps this domain's code
// intact: errs.Wrap is origin-wins, so passing an *errs.Error cause first would
// let it hijack the verdict. A nil cause yields the sentinel with only the
// caller's fields.
func wrapAs(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause contributes no field — keep only what the caller supplied.
	if cause == nil {
		//: wrap for the fields alone.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: carry the cause message as a log-only field so it stays diagnosable.
	return errs.Wrap(sentinel, errs.WrapParams{}, append(fields, errs.String("cause", cause.Error()))...)
}
