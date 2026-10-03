// Package validation — hosts ViolationValue, the located statement that one
// value failed one rule.
package validation

import "github.com/kitsunium/sdk/internal/kernel/errs"

// ViolationValue is one located failure: WHERE it happened, WHICH rule it
// broke, a wire-safe explanation, and a routable [errs.Code].
//
// It is deliberately NOT an error. A validation normally produces several of
// them, and errors.Join of five would render five bracket headers, lose the
// ordering and bury the paths — while a single error would have to pick one
// violation to be about. The aggregate that IS convertible to an error is
// [ReportValue]; see its Err method.
//
// The zero value is not meaningful; constraints build one per failure.
type ViolationValue struct {
	// Path locates the offending value inside the structure that was
	// validated, in the grammar of [JoinField] / [JoinIndex] —
	// "user.addresses[2].zip". [RootPath] (the empty string) means the value
	// as a whole, which is what a cross-field rule reports when no single
	// field is at fault.
	Path string
	// Rule names the constraint that refused, in lower case —
	// "required", "between", "length", "one_of", "pattern". It is the stable
	// identifier a caller switches on when the Code granularity is too coarse,
	// and the key an application uses to look up its own translated message.
	Rule string
	// Message explains the rule in one wire-safe line: no newline, no private
	// data, and — the part that is a security property rather than a style
	// choice — NEVER the rejected value itself. A validation message is the
	// one error message in a service designed to reach the end user, so a
	// message that echoed the value would exfiltrate whatever the caller was
	// unwise enough to validate: a password, a token, a card number. It names
	// the rule and the bound instead ("must be between 8 and 64 characters").
	Message string
	// Code is the dotted-quad identity of the rule that refused, so a caller
	// routes on it with errs.HasCode / errs.NewPrefixMatcher instead of
	// string-matching Rule. The built-in constraints own 0.3.47.*; a
	// consumer-written Constraint SHOULD carry a Code in the third-party
	// Major range (ADR 0019, 0x40-0x7F).
	Code errs.Code
}
