// Package errs — meta-codes (Layer=0 reserved for this file).
// Used in Define panic messages and in internal bootstrap errors
// (newValidationError). These codes are documentary: they are NOT
// returned from emitter APIs as sentinel *Error values.
//
// Layer=0 is ENFORCED reserved for this package by validateDefineArgs —
// any Define call with Layer==0 AND code not in this whitelist will panic.
//
// Constants are typed as Code so Define call sites accept them directly
// and downstream comparisons with Error.CodeValue() are type-safe.
package errs

const (
	// CodeInvalidCode identifies a Define call whose numeric code is zero,
	// overflows int32-positive range, or violates the Layer=0 reserved rule.
	CodeInvalidCode Code = iota + 0x00_00_00_01 // 0.0.0.1

	// CodeInvalidReason identifies a Define call whose Reason is empty or
	// does not match the SCREAMING_SNAKE convention.
	CodeInvalidReason // 0.0.0.2

	// CodeInvalidPublic identifies a Define call whose Public message is
	// empty, exceeds 120 runes, or contains a newline.
	CodeInvalidPublic // 0.0.0.3

	// CodeInvalidPrivate identifies a Define call whose Private message is
	// empty. Introduced by ADR 0005 to close the validate.go bug where
	// CodeInvalidPublic was erroneously cited for Private-field failures.
	CodeInvalidPrivate // 0.0.0.4

	// CodeInvalidCodeString identifies a ParseCode failure (malformed
	// canonical dotted-quad input).
	CodeInvalidCodeString // 0.0.0.5

	// CodeInvalidWrapParams identifies a Wrap-time validation failure on
	// caller-supplied WrapParams. Wrap returns an *Error carrying this
	// code instead of panicking (runtime path, not init-time).
	CodeInvalidWrapParams // 0.0.0.6
)
