// Package validation — the single place a ViolationValue is minted, so every
// built-in constraint reports the same shape.
package validation

import (
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// one builds a single-violation report. Every built-in constraint reports at
// most one violation per value: "too short AND not in the set" is two rules,
// and the caller composed both, so the composition — not the constraint —
// is what accumulates.
//
// The message NEVER contains the rejected value. That is a security property,
// not a style rule: a validation message is the one error message in a service
// designed to reach the end user, so echoing the value would exfiltrate
// whatever was validated — a password, a token, a card number.
func one(path, rule, message string, code errs.Code) corevalidation.ReportValue {
	//: exactly one violation, carrying where / which / why / how to route.
	return corevalidation.ReportValue{{
		Path:    path,
		Rule:    rule,
		Message: message,
		Code:    code,
	}}
}
