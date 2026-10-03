// Package id — declares the sentinels of the domain: those the Generator
// facade returns, and those the concrete schemes raise. A registry sentinel's
// name equals its errs.Define Reason in SCREAMING_SNAKE form; a scheme
// sentinel's Reason is its Code constant's name, minus Code, in the same form
// (ADR 0020's namespaced derivation: CodeIDMalformed → ID_MALFORMED).
package id

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// UnknownScheme is returned when no Generator is registered under the
	// requested Scheme — typically a missing blank-import of the scheme.
	UnknownScheme = errs.Define(CodeUnknownScheme, "UNKNOWN_SCHEME",
		"No identifier generator is registered under that scheme",
		"core/app/id.New: scheme absent from registry; blank-import the scheme's package to register it")

	// DuplicateRegistration is the boot-time panic sentinel for the Generator
	// registry: a nil generator or a distinct generator claiming a taken Scheme.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A generator is already registered under that scheme",
		"core/app/id.Register: a distinct generator already claims this scheme, or a nil generator was supplied")

	// The schemes' outcomes, raised by internal/service/app/id. They are declared
	// here, with the registry's, so that the domain's codes and sentinels are in
	// one place (ADR 0160).

	// EntropyFailed wraps a crypto/rand.Read failure while drawing id bytes.
	EntropyFailed = errs.Define(CodeIDEntropyFailed, "ID_ENTROPY_FAILED",
		"Identifier generation failed to read secure random bytes",
		"service/app/id: crypto/rand.Read returned an error")

	// ClockBackwards is returned when the snowflake clock regressed past the
	// recoverable sequence window.
	ClockBackwards = errs.Define(CodeIDClockBackwards, "ID_CLOCK_BACKWARDS",
		"Identifier generation observed the clock moving backwards",
		"service/app/id: snowflake clock regressed beyond the same-millisecond sequence space")

	// ClockStalled is returned when the snowflake same-millisecond overflow wait
	// gave up because the clock stopped advancing.
	ClockStalled = errs.Define(CodeIDClockStalled, "ID_CLOCK_STALLED",
		"Identifier generation gave up waiting for the clock to advance",
		"service/app/id: snowflake overflow wait exceeded its spin budget without the clock ticking")

	// InvalidSize is returned by NewNanoID when the requested identifier length
	// is not positive. Carries a "size" field.
	InvalidSize = errs.Define(CodeIDInvalidSize, "ID_INVALID_SIZE",
		"Identifier generation requires a positive identifier length",
		"service/app/id: NewNanoID received a size <= 0; a zero-length identifier identifies nothing")

	// InvalidPrefix is returned by NewTypeID/FormatTypeID/ParseTypeID when the
	// type prefix is empty or breaks the lowercase-ASCII shape. Carries a "rule"
	// field naming the clause that failed, and never echoes the prefix.
	InvalidPrefix = errs.Define(CodeIDInvalidPrefix, "ID_INVALID_PREFIX",
		"Identifier generation requires a valid lowercase type prefix",
		"service/app/id: a TypeID prefix is 1..63 chars of [a-z], with '_' allowed only between two letters")

	// Malformed is returned by ParseKSUID/ParseTypeID/FormatTypeID when the
	// input is not a well-formed rendering. Carries a "rule" field naming which
	// check fired (length / alphabet / overflow), never the input itself.
	Malformed = errs.Define(CodeIDMalformed, "ID_MALFORMED",
		"The identifier is not well formed for its scheme",
		"service/app/id: a parse rejected the input's length, alphabet, or magnitude")

	// TimestampRange is returned when the clock sits outside the window a
	// scheme's timestamp field can represent (KSUID: 2014-05-13 .. 2150-06-19).
	TimestampRange = errs.Define(CodeIDTimestampRange, "ID_TIMESTAMP_RANGE",
		"Identifier generation observed a clock outside the scheme's timestamp range",
		"service/app/id: the KSUID 32-bit second counter cannot represent this clock reading")
)
