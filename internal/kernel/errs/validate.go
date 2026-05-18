// Package errs — centralises the runtime structural checks
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

// maxInt32Positive caps the dotted-quad Code at the largest non-negative
// int32 value so the deprecated Code() int accessor never wraps to a
// negative integer.
const maxInt32Positive uint32 = 0x7FFFFFFF

// Package-level mutable state — all package `var` declarations live in one
// grouped block to keep the static surface easy to audit.
var (
	// metaCodeAllowed is the whitelist — only these Codes may have Layer == 0.
	// Anything else with Layer==0 is rejected by validateCode as structurally
	// invalid (Layer=0 reserved for this package's meta-codes per ADR 0005).
	metaCodeAllowed = map[Code]struct{}{
		CodeInvalidCode:       {},
		CodeInvalidReason:     {},
		CodeInvalidPublic:     {},
		CodeInvalidPrivate:    {},
		CodeInvalidCodeString: {},
		CodeInvalidWrapParams: {},
	}
	// errInvalidCode — code violates the dotted-quad rules. errInvalid*
	// sentinels are kept alive (via the trailing `_` slice below) so that
	// any external caller that imports them still compiles, even though the
	// validateDefineArgs refactor now returns *Error values instead.
	errInvalidCode = errors.New("errs.Define: invalid code")
	// errInvalidReason — Reason does not match SCREAMING_SNAKE.
	errInvalidReason = errors.New("errs.Define: invalid reason")
	// errInvalidPublic — Public is empty, too long, or contains a newline.
	errInvalidPublic = errors.New("errs.Define: invalid public message")
	// errInvalidPrivate — Private is empty.
	errInvalidPrivate = errors.New("errs.Define: invalid private message")
	// _ pins the four sentinels against the unused-variable check.
	_ = []error{errInvalidCode, errInvalidReason, errInvalidPublic, errInvalidPrivate}
)

// validateDefineArgs applies Define's structural rules.
func validateDefineArgs(code Code, reason, public, private string) *Error {
	//: run the four structural checks in their documented order; first
	//: failure short-circuits so Define reports the earliest violation.
	if vErr := validateCode(code); vErr != nil {
		//: surface the code-shape failure before any string-shape checks.
		return vErr
	}
	//: reason precedes Public/Private because it is the most-cited identifier
	//: in downstream telemetry and the easiest to mis-spell at the call site.
	if vErr := validateReason(reason); vErr != nil {
		//: surface the reason-shape failure.
		return vErr
	}
	//: Public is the wire-safe message — validate before Private so the
	//: structurally-required public surface is checked before the log-only one.
	if vErr := validatePublic(public); vErr != nil {
		//: surface the public-message failure.
		return vErr
	}
	//: Private is the log-only message; only emptiness is validated here.
	if vErr := validatePrivate(private); vErr != nil {
		//: surface the private-message failure.
		return vErr
	}
	//: all four checks passed — nil signals success to the caller.
	return nil
}

// validateCode checks the dotted-quad Code. Rules (see ADR 0005):
// 1. code != 0
// 2. uint32(code) <= 0x7FFF_FFFF (safe int32 round-trip)
// 3. Layer(code) != 0 unless code is in metaCodeAllowed
func validateCode(code Code) *Error {
	//: reject zero — it is the sentinel "no code" value, never valid.
	if code == 0 {
		//: emit the canonical zero-code rejection with the offending value.
		return newValidationError(CodeInvalidCode, "INVALID_CODE",
			fmt.Sprintf("code must be non-zero, got %d", int(uint32(code))))
	}
	//: reject anything that would overflow the signed int range when the
	//: deprecated Code() int accessor is used. Our scheme keeps Major<=9
	//: for the foreseeable future, so the cap is comfortably far away.
	if uint32(code) > maxInt32Positive {
		//: emit the int32-overflow rejection with the offending hex value.
		return newValidationError(CodeInvalidCode, "INVALID_CODE",
			fmt.Sprintf("code must fit int32 positive range, got %#08x", uint32(code)))
	}
	//: Layer==0 is reserved for the errs-internal meta-codes.
	if code.Layer() == 0 {
		//: only whitelisted meta-codes may have Layer==0; everything else
		//: is a layer-misallocation bug at the call site.
		if _, allowed := metaCodeAllowed[code]; !allowed {
			//: emit the reserved-layer rejection with the offending Code string.
			return newValidationError(CodeInvalidCode, "INVALID_CODE",
				fmt.Sprintf("Layer=0 reserved for meta-codes; got %s", code.String()))
		}
	}
	//: all three Code rules passed.
	return nil
}

// validateReason checks the SCREAMING_SNAKE_CASE shape of Reason.
func validateReason(reason string) *Error {
	//: reject anything that is not a non-empty SCREAMING_SNAKE identifier.
	if !isScreamingSnake(reason) {
		//: emit the canonical bad-reason failure with the offending value
		//: and the documented regex so the call site can self-diagnose.
		return newValidationError(CodeInvalidReason, "INVALID_REASON",
			fmt.Sprintf("reason %q must match %s", reason, reasonPattern))
	}
	//: reason matches the pattern.
	return nil
}

// validatePublic checks Public is non-empty, capped, and newline-free.
func validatePublic(public string) *Error {
	//: reject empty Public — every error MUST carry a wire-safe message.
	if public == "" {
		//: emit the empty-public failure.
		return newValidationError(CodeInvalidPublic, "INVALID_PUBLIC",
			"public must not be empty")
	}
	//: enforce the rune cap so a runaway message cannot bloat HTTP bodies
	//: or log lines downstream.
	if utf8.RuneCountInString(public) > maxPublicRunes {
		//: emit the over-cap failure with both the cap and the actual size.
		return newValidationError(CodeInvalidPublic, "INVALID_PUBLIC",
			fmt.Sprintf("public exceeds %d runes (got %d)",
				maxPublicRunes, utf8.RuneCountInString(public)))
	}
	//: reject embedded newlines so Public stays usable on single-line log
	//: writers and HTTP status text.
	if containsNewline(public) {
		//: emit the newline-in-public failure.
		return newValidationError(CodeInvalidPublic, "INVALID_PUBLIC",
			"public must not contain a newline")
	}
	//: all three Public rules passed.
	return nil
}

// validatePrivate checks Private is non-empty. v5 bug fix: this now cites
// CodeInvalidPrivate (not CodeInvalidPublic as pre-ADR-0005).
func validatePrivate(private string) *Error {
	//: reject empty Private — operators MUST receive a log-only context line.
	if private == "" {
		//: emit the empty-private failure with the corrected meta-code.
		return newValidationError(CodeInvalidPrivate, "INVALID_PRIVATE",
			"private must not be empty")
	}
	//: Private is non-empty.
	return nil
}

// containsNewline reports whether s contains any newline rune.
func containsNewline(s string) bool {
	//: scan rune-by-rune so a CR/LF anywhere — not just at the boundary —
	//: trips the check.
	for _, r := range s {
		//: match both LF and CR to cover the full set of line terminators
		//: that single-line writers care about.
		if r == '\n' || r == '\r' {
			//: a single hit is enough to disqualify the message.
			return true
		}
	}
	//: no terminator found.
	return false
}

// isScreamingSnake reports whether s matches reasonPattern without pulling
// regexp into the kernel package.
func isScreamingSnake(s string) bool {
	//: an empty string can never satisfy the leading-uppercase rule.
	if s == "" {
		//: reject empty as the fastest path.
		return false
	}
	//: walk every rune so the per-position rule can be applied.
	for i, r := range s {
		//: delegate the per-position check; first-rune rule differs from tail.
		if !isValidReasonRune(i, r) {
			//: a single invalid rune disqualifies the whole identifier.
			return false
		}
	}
	//: every rune passed the per-position check.
	return true
}

// isValidReasonRune reports whether r is acceptable at position i inside a
// Reason identifier.
func isValidReasonRune(i int, r rune) bool {
	//: the first rune is restricted to uppercase ASCII to keep Reason
	//: identifiers searchable and pattern-matchable.
	if i == 0 {
		//: tight check inline — avoids a helper-call on the hot path.
		return r >= 'A' && r <= 'Z'
	}
	//: tail runes admit uppercase letters, ASCII digits, and underscore.
	return isUpperLetter(r) || isDigit(r) || r == '_'
}

// isUpperLetter reports whether r is an uppercase ASCII letter.
func isUpperLetter(r rune) bool {
	//: inclusive range check against the ASCII uppercase block.
	return r >= 'A' && r <= 'Z'
}

// isDigit reports whether r is an ASCII digit.
func isDigit(r rune) bool {
	//: inclusive range check against the ASCII digit block.
	return r >= '0' && r <= '9'
}
