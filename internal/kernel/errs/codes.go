// Package errs: codes.go — range 1000-1099. The errs package itself is an
// emitter infrastructure package used by every other emitter in the SDK;
// the 1000-1099 range is reserved for documentary meta-codes that appear
// only inside Define panic messages so operators can grep logs for e.g.
// "[1001 INVALID_CODE]". No sentinel *Error values are exported for these
// codes — defining them via Define itself would be circular.
package errs

// range: 1000-1099

// CodeInvalidCode identifies a Define call whose numeric code is either
// zero or outside the 1000+ layered range. Cited in the panic message.
const CodeInvalidCode int = 1001

// CodeInvalidReason identifies a Define call whose Reason is empty or
// does not match the SCREAMING_SNAKE convention enforced by the SDK.
const CodeInvalidReason int = 1002

// CodeInvalidPublic identifies a Define call whose Public message is
// empty, exceeds 120 runes, or contains a newline. Literal-ness of the
// argument is enforced separately by the AST audit, not at runtime.
const CodeInvalidPublic int = 1003
