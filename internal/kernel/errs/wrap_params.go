// Package errs — hosts WrapParams in its own file so error.go keeps a
// single exported struct (one-struct-per-file convention).
package errs

// WrapParams groups the extra metadata Wrap needs when the cause is NOT
// already an *Error. Keeping them in a struct stays below the SDK's
// 5-parameter ceiling for Wrap and gives call sites named fields.
type WrapParams struct {
	// Code is the dotted-quad identifier assigned to the wrapping Error.
	Code Code
	// Reason is the SCREAMING_SNAKE stable identifier of the wrapping Error.
	Reason string
	// Public is the wire-safe message (string literal at source level).
	Public string
	// Private is the log-only detailed message.
	Private string
	// ExitCode optionally overrides the POSIX exit status of the wrapping
	// Error (default 70 EX_SOFTWARE); the zero value keeps the default. It
	// lets a stdlib cause be wrapped into an Error that carries the same
	// exit semantics as a sibling errs.Define sentinel — the origin-wins
	// path already inherits a cause Error's exit override, but the
	// stdlib-cause path has no Error to inherit from.
	ExitCode int
}
