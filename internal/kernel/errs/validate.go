// Package errs: validate.go centralises the runtime structural checks
// applied by Define. Factoring the logic out of Define lets tests cover
// every rule as a plain function call without subprocess fixtures for
// package-init panics.
package errs

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// minLayeredCode is the smallest code value a caller may pass to Define;
// any lower value would not fit the 1xxx-4xxx layered scheme.
const minLayeredCode int = 1000

// maxPublicRunes bounds the size of the wire-safe Public message so no
// single error can blow up logs or HTTP responses.
const maxPublicRunes int = 120

// reasonPattern documents the SCREAMING_SNAKE_CASE shape expected for Reason.
// Runtime enforcement happens via a hand-rolled scan to keep this package
// stdlib-only (no regexp dependency).
const reasonPattern string = "^[A-Z][A-Z0-9_]*$"

// errInvalid* sentinels are returned by validateDefineArgs when Define is
// called with structurally invalid arguments. They are wrapped with fmt.Errorf
// so callers can both match via errors.Is and read a detailed message.
var (
	// errInvalidCode — code is zero or below minLayeredCode.
	errInvalidCode = errors.New("errs.Define: invalid code")
	// errInvalidReason — Reason does not match SCREAMING_SNAKE.
	errInvalidReason = errors.New("errs.Define: invalid reason")
	// errInvalidPublic — Public is empty, too long, or contains a newline.
	errInvalidPublic = errors.New("errs.Define: invalid public message")
	// errInvalidPrivate — Private is empty.
	errInvalidPrivate = errors.New("errs.Define: invalid private message")
)

// validateDefineArgs applies Define's structural rules.
//
// Params:
//   - code: numeric code; must be non-zero and greater or equal minLayeredCode.
//   - reason: stable identifier; must match reasonPattern.
//   - public: wire-safe message; non-empty, capped, no newline.
//   - private: log-only message; non-empty.
//
// Returns:
//   - error: nil on success, otherwise a wrapped sentinel whose message
//     cites the matching documentary meta-code (1001/1002/1003).
func validateDefineArgs(code int, reason, public, private string) (err error) {
	//: run the four structural checks in their documented order.
	if vErr := validateCode(code); vErr != nil {
		//: first failure short-circuits to surface the relevant meta-code.
		return vErr
	}
	//: next check the stable identifier.
	if vErr := validateReason(reason); vErr != nil {
		//: Reason failure surfaces INVALID_REASON with the offending value.
		return vErr
	}
	//: Public has its own dedicated helper due to rune counting.
	if vErr := validatePublic(public); vErr != nil {
		//: Public failure — length, newline, or emptiness.
		return vErr
	}
	//: Private is the simplest rule but still goes through a helper for symmetry.
	if vErr := validatePrivate(private); vErr != nil {
		//: Private empty — Define refuses silent emission.
		return vErr
	}
	//: all four rules satisfied.
	return nil
}

// validateCode checks the numeric code against the layered scheme.
//
// Params:
//   - code: numeric code to verify.
//
// Returns:
//   - error: nil on success, errInvalidCode wrapped with context otherwise.
func validateCode(code int) (err error) {
	//: reject the zero sentinel and anything below the minimum layered value.
	if code == 0 || code < minLayeredCode {
		//: wrap the sentinel with CodeInvalidCode so grepping logs finds it.
		return fmt.Errorf("%w [%d INVALID_CODE]: code must be non-zero and >= %d, got %d",
			errInvalidCode, CodeInvalidCode, minLayeredCode, code)
	}
	//: code is structurally valid.
	return nil
}

// validateReason checks the SCREAMING_SNAKE_CASE shape of Reason.
//
// Params:
//   - reason: stable identifier to verify.
//
// Returns:
//   - error: nil on success, errInvalidReason wrapped otherwise.
func validateReason(reason string) (err error) {
	//: reject any Reason that does not match the documented alphabet.
	if !isScreamingSnake(reason) {
		//: include both the offending value and the pattern in the message.
		return fmt.Errorf("%w [%d INVALID_REASON]: reason %q must match %s",
			errInvalidReason, CodeInvalidReason, reason, reasonPattern)
	}
	//: Reason is structurally valid.
	return nil
}

// validatePublic checks Public is non-empty, capped, and newline-free.
//
// Params:
//   - public: wire-safe message to verify.
//
// Returns:
//   - error: nil on success, errInvalidPublic wrapped otherwise.
func validatePublic(public string) (err error) {
	//: empty Public messages cannot be surfaced on the wire.
	if public == "" {
		//: surface the meta-code so operators can grep it.
		return fmt.Errorf("%w [%d INVALID_PUBLIC]: public must not be empty",
			errInvalidPublic, CodeInvalidPublic)
	}
	//: count runes so the limit is user-visible characters, not bytes.
	if utf8.RuneCountInString(public) > maxPublicRunes {
		//: report both the cap and the actual count for quick diagnosis.
		return fmt.Errorf("%w [%d INVALID_PUBLIC]: public exceeds %d runes (got %d)",
			errInvalidPublic, CodeInvalidPublic, maxPublicRunes, utf8.RuneCountInString(public))
	}
	//: newlines would break single-line log/HTTP consumers.
	if containsNewline(public) {
		//: reject the candidate with a clear diagnostic.
		return fmt.Errorf("%w [%d INVALID_PUBLIC]: public must not contain a newline",
			errInvalidPublic, CodeInvalidPublic)
	}
	//: Public is structurally valid.
	return nil
}

// validatePrivate checks Private is non-empty.
//
// Params:
//   - private: log-only message to verify.
//
// Returns:
//   - error: nil on success, errInvalidPrivate wrapped otherwise.
func validatePrivate(private string) (err error) {
	//: Private is intentionally uncapped but must carry at least one character.
	if private == "" {
		//: empty Private would hide the cause from diagnostics.
		return fmt.Errorf("%w [%d INVALID_PRIVATE]: private must not be empty",
			errInvalidPrivate, CodeInvalidPublic)
	}
	//: Private is structurally valid.
	return nil
}

// containsNewline reports whether s contains any newline rune.
//
// Params:
//   - s: candidate string.
//
// Returns:
//   - bool: true iff s contains '\n' or '\r'.
func containsNewline(s string) (ok bool) {
	//: walk the runes so multi-byte characters do not mask a nested newline.
	for _, r := range s {
		//: early-exit on the first newline encountered.
		if r == '\n' || r == '\r' {
			//: signal presence to the caller.
			return true
		}
	}
	//: no newline found in any rune.
	return false
}

// isScreamingSnake reports whether s matches reasonPattern without pulling
// regexp into the kernel package.
//
// Params:
//   - s: candidate string.
//
// Returns:
//   - bool: true iff s is a non-empty SCREAMING_SNAKE identifier.
func isScreamingSnake(s string) (ok bool) {
	//: empty strings cannot be a valid Reason.
	if s == "" {
		//: caller must supply at least one character.
		return false
	}
	//: walk every rune, delegating per-position validation to helpers.
	for i, r := range s {
		//: dispatch to the position-specific validator.
		if !isValidReasonRune(i, r) {
			//: any rejected rune invalidates the whole Reason.
			return false
		}
	}
	//: all runes satisfied the alphabet.
	return true
}

// isValidReasonRune reports whether r is acceptable at position i inside a
// Reason identifier. Extracted out of isScreamingSnake to keep cyclomatic
// complexity of the caller under the SDK's ceiling.
//
// Params:
//   - i: zero-based rune index within the Reason.
//   - r: the rune being validated.
//
// Returns:
//   - bool: true iff r is allowed at position i.
func isValidReasonRune(i int, r rune) (ok bool) {
	//: the first rune must be an uppercase ASCII letter.
	if i == 0 {
		//: narrow A-Z gate for the leading rune.
		return r >= 'A' && r <= 'Z'
	}
	//: subsequent runes may be uppercase, digit, or underscore.
	return isUpperLetter(r) || isDigit(r) || r == '_'
}

// isUpperLetter reports whether r is an uppercase ASCII letter.
//
// Params:
//   - r: candidate rune.
//
// Returns:
//   - bool: true iff r ∈ [A-Z].
func isUpperLetter(r rune) (ok bool) {
	//: direct range compare — no unicode tables needed.
	return r >= 'A' && r <= 'Z'
}

// isDigit reports whether r is an ASCII digit.
//
// Params:
//   - r: candidate rune.
//
// Returns:
//   - bool: true iff r ∈ [0-9].
func isDigit(r rune) (ok bool) {
	//: direct range compare — no unicode tables needed.
	return r >= '0' && r <= '9'
}
