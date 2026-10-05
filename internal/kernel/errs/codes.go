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
