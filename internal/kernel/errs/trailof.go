// Package errs — TrailOf exposes the wrap-trail codes of the deepest *Error in
// a chain, the read-side companion to the Of-family accessors, enabling
// structured log decomposition without reparsing the rendered bracket header.
package errs

import "errors"

// TrailOf returns the wrap-trail codes of the deepest *Error in err's chain,
// newest wrap last, or nil when err carries no *Error.
//
// The trail mirrors the wrap-site chain accumulated as the error was wrapped
// (the origin's own Code is NOT included — it is reached via CodeOf).
func TrailOf(err error) []Code {
	target, ok := errors.AsType[*Error](err)

	//: a chain with no *Error has no trail to report.
	if !ok {
		//: absence path mirrors the Of-family accessors' nil/zero contract.
		return nil
	}

	//: delegate to the Error's own defensive-copy accessor.
	return target.Trail()
}
