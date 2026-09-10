// Package authz — declares the sentinel *errs.Error construction outcomes.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package authz

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel in this file is a
// permanent wiring fault raised while a policy is being ASSEMBLED, never while
// one is being evaluated: the same arguments will be refused forever, and the
// fix is a code change at the call site.
const exitConfig int = 78

// These three carry a Public that describes a server-side configuration fault
// rather than the neutral refusal sentence internal/core/authz uses. That is
// deliberate and is not a leak: a construction error is returned to the
// process that is starting up, on a path where no request exists yet and no
// client is listening. It never becomes a response, so it may be specific.
var (
	// GrantInvalid is returned by NewRBAC for a grant table that could never
	// grant correctly. The fields name the offending row and role.
	GrantInvalid = errs.Define(CodeGrantInvalid, "GRANT_INVALID",
		"The role grant table is not usable and was refused",
		"service/authz: NewRBAC received a grant table it cannot honour; the fields carry the detail, the row index and the role",
		errs.WithExitCode(exitConfig))

	// RuleInvalid is returned by NewABAC for a rule set that could never
	// decide correctly. The fields name the offending rule and its position.
	RuleInvalid = errs.Define(CodeRuleInvalid, "RULE_INVALID",
		"The attribute rule set is not usable and was refused",
		"service/authz: NewABAC received a rule it cannot honour; the fields carry the detail, the rule index and the rule name",
		errs.WithExitCode(exitConfig))

	// ConditionInvalid is returned by a condition constructor whose arguments
	// it cannot honour. It is never an evaluation outcome: an attribute that
	// is absent or of the wrong kind is core/authz's AttributeMissing or
	// AttributeKindMismatch, and both of those are refusals of a REQUEST,
	// while this is a refusal of the RULE.
	ConditionInvalid = errs.Define(CodeConditionInvalid, "CONDITION_INVALID",
		"The condition cannot be built from the given arguments",
		"service/authz: a condition constructor received an empty key, an empty member, a nil condition or an empty set; the fields carry the detail",
		errs.WithExitCode(exitConfig))
)
