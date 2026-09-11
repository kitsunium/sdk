// Package authz — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package authz

import "github.com/kitsunium/sdk/internal/kernel/errs"

// httpForbidden is RFC 9110 403 — the request was understood and the server
// refuses to authorize it. It is the status EVERY outcome in this file carries,
// including the two that are really evaluation faults, because the alternative
// (500 for an unevaluable rule) tells a client to retry a request that will be
// refused identically forever, and tells an attacker which of their inputs the
// policy could not parse.
//
// A framework that would rather answer 404 to hide the resource's existence
// overrides it at the edge. The SDK picks the honest default and does not
// decide the response shape; see internal/core/authz/CLAUDE.md §The frontier.
const httpForbidden int = 403

// exitConfig matches sysexits EX_CONFIG (78). A policy that was assembled
// wrong is a permanent wiring fault: the same composition will refuse every
// request forever, and the fix is a code change, never a retry.
const exitConfig int = 78

// Every sentinel below carries the SAME Public sentence, "Access to the
// requested resource is denied", spelled out at each call site because the
// registry audit requires the argument to be a string literal.
//
// The repetition is the point, and it is a security property rather than an
// oversight: a refusal message that explains itself is a description of the
// policy set, delivered to the party the policy exists to keep out. "You are
// not an admin" names the role model. "Missing attribute department" names an
// attribute and tells the attacker which value to forge next. "Denied by rule
// 7" tells them how many rules there are and which one to work around.
//
// The diagnosis is not lost — it is moved. Every sentinel carries a distinct
// Private, the call site attaches structured fields, and both reach the
// operator through errs.PrivateOf / errs.FieldsOf, which pkg/v1's own docs
// forbid putting on the wire. What crosses the boundary is one sentence that
// is true of every refusal and specific to none — and a test pins that the
// four Publics stay byte-identical.

var (
	// PermissionDenied is the refusal. It is what internal/service/authz.Check
	// returns for an explicit Deny, for an evaluation in which every policy
	// abstained, and for one that could not be completed — three causes, one
	// error, one code, one sentence.
	//
	// The fields distinguish them for the operator: "outcome" is "deny",
	// "abstain" or "unevaluable", and an unevaluable outcome also carries the
	// underlying code and reason. errs.HasCode(err, CodePermissionDenied)
	// therefore answers true for every refusal, which is what a framework
	// routes on.
	PermissionDenied = errs.Define(CodePermissionDenied, "PERMISSION_DENIED",
		"Access to the requested resource is denied",
		"core/authz: the request was refused; the fields carry the outcome, the subject, the action, the resource and — when the evaluation failed — the underlying code",
		errs.WithHTTPStatus(httpForbidden))

	// AttributeMissing is returned by a [Condition] whose rule names an
	// attribute the request does not carry.
	//
	// It exists as its own code because the alternative is the defect this
	// domain was written to prevent: reporting an absent attribute as a failed
	// comparison. `department == "finance"` on a subject with no department is
	// not false — it is unanswerable, and answering false lets a request that
	// carries NO attributes at all walk past every rule of that shape. Every
	// combinator treats it as absorbing, so it cannot be laundered into a
	// grant by a Not, an AnyOf, or a rule whose effect happens to be Deny.
	AttributeMissing = errs.Define(CodeAttributeMissing, "ATTRIBUTE_MISSING",
		"Access to the requested resource is denied",
		"core/authz: a rule named an attribute the request does not carry; the fields name the attribute and the rule",
		errs.WithHTTPStatus(httpForbidden))

	// AttributeKindMismatch is returned by a [Condition] whose attribute is
	// present but carries another kind — a number where the rule compares
	// text, a set where it reads a flag.
	//
	// It is the same failure as an absent attribute wearing a different hat:
	// the comparison did not happen. A kind mismatch is normally a wiring bug
	// on the producing side, so it is reported rather than coerced; coercing
	// would make "42" and 42 the same attribute in one direction and not the
	// other, which is how a rule starts matching requests it was never
	// written for.
	AttributeKindMismatch = errs.Define(CodeAttributeKindMismatch, "ATTRIBUTE_KIND_MISMATCH",
		"Access to the requested resource is denied",
		"core/authz: an attribute is present but not the kind the rule compares; the fields name the attribute, the wanted kind and the kind found",
		errs.WithHTTPStatus(httpForbidden))

	// PolicyMisconfigured is returned when a composition cannot be evaluated
	// because of how it was built: a nil [Policy] among its members, or a
	// member that returned a [Decision] outside the three named states.
	//
	// Both are treated as [Deny] and reported, never skipped. Skipping a nil
	// policy would silently shrink the policy set — the one edit an attacker
	// would most like to make — and treating an out-of-contract Decision as
	// anything but a refusal would let a numeric conversion authorize.
	PolicyMisconfigured = errs.Define(CodePolicyMisconfigured, "POLICY_MISCONFIGURED",
		"Access to the requested resource is denied",
		"core/authz: a policy could not be evaluated as assembled — a nil member, or a decision outside allow/deny/abstain; the fields carry the position and the value",
		errs.WithHTTPStatus(httpForbidden),
		errs.WithExitCode(exitConfig))
)
