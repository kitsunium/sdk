// Package console: codes.go — range 3400-3499 reserved for the console
// Sink. Codes are declared at source as typed constants; the errs registry
// audit verifies uniqueness and range membership.
package console

// range: 3400-3499

// CodeWriterNil identifies a New call made with a nil io.Writer.
const CodeWriterNil int = 3401

// CodeCtxCancelled identifies a Write call whose context was already done.
const CodeCtxCancelled int = 3410

// CodeWriteFailed identifies a Write call whose underlying io.Writer
// returned a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed int = 3420
