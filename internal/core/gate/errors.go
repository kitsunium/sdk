// Package gate — the one refusal this domain mints.
//
// It is one rather than several on purpose. Every fault a policy can carry is
// the same kind of fault — somebody wrote a policy that cannot do its job — and
// Validate joins them so a caller fixing one sees all of them. Splitting the
// range would give a caller several codes to branch on for a condition with a
// single response: fix the policy and restart.
package gate

import "github.com/kitsunium/sdk/internal/kernel/errs"

// misconfigured builds the refusal every Validate fault carries, so the code,
// reason and public text are written once.
//
// It takes detail — what is wrong, for the operator's own log, naming no value
// a caller supplied beyond the command path already in their policy — and
// returns the typed refusal.
func misconfigured(detail string) error {
	//: one sentinel shape, one private detail per call site.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodePolicyInvalid,
		Reason:  "POLICY_INVALID",
		Public:  "the gate policy is misconfigured",
		Private: "core/gate: " + detail,
	})
}
