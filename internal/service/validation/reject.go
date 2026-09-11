// Package validation — the refusal helpers. Every refusal names what was
// wrong in structured fields; the Public message stays a fixed literal.
package validation

import (
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxEchoRunes caps how much of the caller's own text a refusal echoes into a
// field. A tag and a field name are the caller's source, not untrusted input,
// so echoing them is what makes the refusal actionable — but an unbounded echo
// turns one bad tag into a log entry nobody can read.
const maxEchoRunes int = 64

// ellipsis marks a clipped echo.
const ellipsis string = "…"

// rejectConstraint refuses a constraint at CONSTRUCTION, which is the whole
// point: a bound that cannot be honoured must never become a validator that
// quietly rejects — or quietly accepts — everything it is shown (ADR 0031).
func rejectConstraint(rule, clause string) error {
	//: the rule and the clause are the two things that make the fix obvious.
	return errs.Wrap(corevalidation.ConstraintMisconfigured, errs.WrapParams{},
		errs.String("rule", rule), errs.String("clause", clause))
}

// rejectRule refuses a validate struct tag at COMPILE time.
func rejectRule(field, rule, clause string) error {
	//: naming the construct is the point — "invalid tag" would leave the
	//: caller guessing whether the rule is wrong or merely unsupported here.
	return errs.Wrap(InvalidRule, errs.WrapParams{},
		errs.String("field", clip(field)), errs.String("rule", clip(rule)),
		errs.String("clause", clause))
}

// rejectTarget refuses a Struct[T] whose T has no fields to walk.
func rejectTarget(kind string) error {
	//: the kind that was seen is the whole diagnosis.
	return errs.Wrap(UnsupportedTarget, errs.WrapParams{}, errs.String("kind", kind))
}

// clip shortens text to maxEchoRunes runes, marking the truncation. It counts
// RUNES, not bytes, so a multi-byte identifier is never cut mid-character.
func clip(text string) string {
	//: the common case is short enough to travel whole.
	runes := []rune(text)
	//: no truncation needed.
	if len(runes) <= maxEchoRunes {
		//: hand it back unchanged.
		return text
	}
	//: cut on a rune boundary and mark it.
	return string(runes[:maxEchoRunes]) + ellipsis
}
