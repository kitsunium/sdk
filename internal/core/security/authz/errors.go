// Package authz — declares the sentinel *errs.Error outcomes: the four
// verdicts of the port, and the three construction refusals of the engine in
// internal/service/security/authz (ADR 0160). Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package authz

import "github.com/kitsunium/sdk/internal/kernel/errs"

// httpForbidden is RFC 9110 403 — the request was understood and the server
// refuses to authorize it. It is the status EVERY verdict of the port carries,
// including the two that are really evaluation faults, because the alternative
// (500 for an unevaluable rule) tells a client to retry a request that will be
// refused identically forever, and tells an attacker which of their inputs the
// policy could not parse.
//
// A framework that would rather answer 404 to hide the resource's existence
// overrides it at the edge. The SDK picks the honest default and does not
// decide the response shape; see internal/core/security/authz/CLAUDE.md §The frontier.
const httpForbidden int = 403

// exitConfig matches sysexits EX_CONFIG (78). A policy that was assembled
// wrong is a permanent wiring fault: the same composition will refuse every
// request forever, and the fix is a code change, never a retry. The engine's
// three construction refusals carry it for the same reason.
const exitConfig int = 78

// Every verdict in the first block below carries the SAME Public sentence,
// "Access to the requested resource is denied", spelled out at each call site
// because the registry audit requires the argument to be a string literal.
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
	// PermissionDenied is the refusal. It is what internal/service/security/authz.Check
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
		"core/security/authz: the request was refused; the fields carry the outcome, the subject, the action, the resource and — when the evaluation failed — the underlying code",
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
		"core/security/authz: a rule named an attribute the request does not carry; the fields name the attribute and the rule",
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
		"core/security/authz: an attribute is present but not the kind the rule compares; the fields name the attribute, the wanted kind and the kind found",
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
		"core/security/authz: a policy could not be evaluated as assembled — a nil member, or a decision outside allow/deny/abstain; the fields carry the position and the value",
		errs.WithHTTPStatus(httpForbidden),
		errs.WithExitCode(exitConfig))

	// The engine's construction refusals (0.3.56.*). They are raised by
	// internal/service/security/authz while a policy is being ASSEMBLED — their
	// Private names that package, the one that raises them — and declared here
	// since ADR 0160, so every code of the domain is in one place.
	//
	// These three carry a Public that describes a server-side configuration
	// fault rather than the neutral refusal sentence the verdicts above use.
	// That is deliberate and is not a leak: a construction error is returned to
	// the process that is starting up, on a path where no request exists yet
	// and no client is listening. It never becomes a response, so it may be
	// specific. Every one is a permanent wiring fault — the same arguments are
	// refused forever, and the fix is a code change at the call site — so all
	// three carry EX_CONFIG.

	// GrantInvalid is returned by NewRBAC for a grant table that could never
	// grant correctly. The fields name the offending row and role.
	GrantInvalid = errs.Define(CodeGrantInvalid, "GRANT_INVALID",
		"The role grant table is not usable and was refused",
		"service/security/authz: NewRBAC received a grant table it cannot honour; the fields carry the detail, the row index and the role",
		errs.WithExitCode(exitConfig))

	// RuleInvalid is returned by NewABAC for a rule set that could never
	// decide correctly. The fields name the offending rule and its position.
	RuleInvalid = errs.Define(CodeRuleInvalid, "RULE_INVALID",
		"The attribute rule set is not usable and was refused",
		"service/security/authz: NewABAC received a rule it cannot honour; the fields carry the detail, the rule index and the rule name",
		errs.WithExitCode(exitConfig))

	// ConditionInvalid is returned by a condition constructor whose arguments
	// it cannot honour. It is never an evaluation outcome: an attribute that
	// is absent or of the wrong kind is AttributeMissing or
	// AttributeKindMismatch, and both of those are refusals of a REQUEST,
	// while this is a refusal of the RULE.
	ConditionInvalid = errs.Define(CodeConditionInvalid, "CONDITION_INVALID",
		"The condition cannot be built from the given arguments",
		"service/security/authz: a condition constructor received an empty key, an empty member, a nil condition or an empty set; the fields carry the detail",
		errs.WithExitCode(exitConfig))
)
