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

// maxPublicRunes bounds the size of the wire-safe Public message so no
// single error can blow up logs or HTTP responses.
const maxPublicRunes int = 120

// reasonPattern documents the SCREAMING_SNAKE_CASE shape expected for Reason.
const reasonPattern string = "^[A-Z][A-Z0-9_]*$"

// Meta-code whitelist — only these Codes may have Layer == 0.
// Anything else with Layer==0 is rejected by validateCode as structurally
// invalid (Layer=0 reserved for this package's meta-codes per ADR 0005).
var metaCodeAllowed = map[Code]struct{}{
	CodeInvalidCode:       {},
	CodeInvalidReason:     {},
	CodeInvalidPublic:     {},
	CodeInvalidPrivate:    {},
	CodeInvalidCodeString: {},
	CodeInvalidWrapParams: {},
}

// errInvalid* sentinels are returned by validateDefineArgs when Define is
// called with structurally invalid arguments.
var (
	// errInvalidCode — code violates the dotted-quad rules.
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
//   - code: typed Code identifier; must be non-zero, fit int32-positive, and
//     satisfy the Layer rule (Layer==0 only for the meta-code whitelist).
//   - reason: stable identifier; must match reasonPattern.
//   - public: wire-safe message; non-empty, capped, no newline.
//   - private: log-only message; non-empty.
//
// Returns:
//   - *Error: nil on success; otherwise a bootstrap-only *Error built via
//     newValidationError (NEVER via Define — avoids init recursion).
func validateDefineArgs(code Code, reason, public, private string) (err *Error) {
	//: run the four structural checks in their documented order.
	if vErr := validateCode(code); vErr != nil {
		return vErr
	}
	if vErr := validateReason(reason); vErr != nil {
		return vErr
	}
	if vErr := validatePublic(public); vErr != nil {
		return vErr
	}
	if vErr := validatePrivate(private); vErr != nil {
		return vErr
	}
	return nil
}

// validateCode checks the dotted-quad Code. Rules (see ADR 0005):
//  1. code != 0
//  2. uint32(code) <= 0x7FFF_FFFF (safe int32 round-trip)
//  3. Layer(code) != 0 unless code is in metaCodeAllowed
//
// Params:
//   - code: Code to verify.
//
// Returns:
//   - *Error: nil on success; structural failure otherwise.
func validateCode(code Code) (err *Error) {
	//: reject zero — it is the sentinel "no code" value, never valid.
	if code == 0 {
		return newValidationError(CodeInvalidCode, "INVALID_CODE",
			fmt.Sprintf("code must be non-zero, got %d", int(uint32(code))))
	}
	//: reject anything that would overflow the signed int range when the
	//: deprecated Code() int accessor is used. Our scheme keeps Major<=9
	//: for the foreseeable future, so the cap is comfortably far away.
	if uint32(code) > 0x7FFF_FFFF {
		return newValidationError(CodeInvalidCode, "INVALID_CODE",
			fmt.Sprintf("code must fit int32 positive range, got %#08x", uint32(code)))
	}
	//: Layer==0 is reserved for the errs-internal meta-codes.
	if code.Layer() == 0 {
		if _, allowed := metaCodeAllowed[code]; !allowed {
			return newValidationError(CodeInvalidCode, "INVALID_CODE",
				fmt.Sprintf("Layer=0 reserved for meta-codes; got %s", code.String()))
		}
	}
	return nil
}

// validateReason checks the SCREAMING_SNAKE_CASE shape of Reason.
//
// Params:
//   - reason: stable identifier to verify.
//
// Returns:
//   - *Error: nil on success; structural failure otherwise.
func validateReason(reason string) (err *Error) {
	if !isScreamingSnake(reason) {
		return newValidationError(CodeInvalidReason, "INVALID_REASON",
			fmt.Sprintf("reason %q must match %s", reason, reasonPattern))
	}
	return nil
}

// validatePublic checks Public is non-empty, capped, and newline-free.
//
// Params:
//   - public: wire-safe message to verify.
//
// Returns:
//   - *Error: nil on success; structural failure otherwise.
func validatePublic(public string) (err *Error) {
	if public == "" {
		return newValidationError(CodeInvalidPublic, "INVALID_PUBLIC",
			"public must not be empty")
	}
	if utf8.RuneCountInString(public) > maxPublicRunes {
		return newValidationError(CodeInvalidPublic, "INVALID_PUBLIC",
			fmt.Sprintf("public exceeds %d runes (got %d)",
				maxPublicRunes, utf8.RuneCountInString(public)))
	}
	if containsNewline(public) {
		return newValidationError(CodeInvalidPublic, "INVALID_PUBLIC",
			"public must not contain a newline")
	}
	return nil
}

// validatePrivate checks Private is non-empty. v5 bug fix: this now cites
// CodeInvalidPrivate (not CodeInvalidPublic as pre-ADR-0005).
//
// Params:
//   - private: log-only message to verify.
//
// Returns:
//   - *Error: nil on success; structural failure otherwise.
func validatePrivate(private string) (err *Error) {
	if private == "" {
		return newValidationError(CodeInvalidPrivate, "INVALID_PRIVATE",
			"private must not be empty")
	}
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
	for _, r := range s {
		if r == '\n' || r == '\r' {
			return true
		}
	}
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
	if s == "" {
		return false
	}
	for i, r := range s {
		if !isValidReasonRune(i, r) {
			return false
		}
	}
	return true
}

// isValidReasonRune reports whether r is acceptable at position i inside a
// Reason identifier.
//
// Params:
//   - i: zero-based rune index within the Reason.
//   - r: the rune being validated.
//
// Returns:
//   - bool: true iff r is allowed at position i.
func isValidReasonRune(i int, r rune) (ok bool) {
	if i == 0 {
		return r >= 'A' && r <= 'Z'
	}
	return isUpperLetter(r) || isDigit(r) || r == '_'
}

// isUpperLetter reports whether r is an uppercase ASCII letter.
func isUpperLetter(r rune) (ok bool) { return r >= 'A' && r <= 'Z' }

// isDigit reports whether r is an ASCII digit.
func isDigit(r rune) (ok bool) { return r >= '0' && r <= '9' }

// _ keeps the sentinel errors alive for potential future use (they're
// currently not consumed anywhere after the validateDefineArgs refactor
// to *Error returns, but removing them would be a source-visible API
// break for any external caller that imports them).
var _ = []error{errInvalidCode, errInvalidReason, errInvalidPublic, errInvalidPrivate}
