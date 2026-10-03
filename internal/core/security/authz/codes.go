// Package authz — ranges 0.2.26.* (the domain's verdicts) and 0.3.56.* (the
// engine's construction refusals) — ADR 0057, declared here since ADR 0160.
package authz

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.26.0 - 0.2.26.255

// CodePermissionDenied identifies the single refusal this domain produces. It
// is the code every non-[Allow] outcome converts to — an explicit [Deny], an
// all-[Abstain] evaluation, a policy that could not be evaluated — because a
// caller who can tell those apart from the outside learns the shape of the
// policy set.
const CodePermissionDenied errs.Code = 0x00_02_1A_01 // 0.2.26.1

// CodeAttributeMissing identifies a rule that named an attribute the request
// does not carry. It is an evaluation FAILURE, never a false comparison: the
// rule did not run, so nothing about it was satisfied.
const CodeAttributeMissing errs.Code = 0x00_02_1A_02 // 0.2.26.2

// CodeAttributeKindMismatch identifies an attribute that is present but not
// the kind the rule compares — a text rule against a number, a flag rule
// against a set. Like an absent attribute, it means the rule did not run.
const CodeAttributeKindMismatch errs.Code = 0x00_02_1A_03 // 0.2.26.3

// CodePolicyMisconfigured identifies a policy that cannot be evaluated because
// of how it was assembled: a nil [Policy] in a composition, or a [Policy] that
// returned a [Decision] outside the three named states.
const CodePolicyMisconfigured errs.Code = 0x00_02_1A_04 // 0.2.26.4

// range: 0.3.56.0 - 0.3.56.255
//
// The construction refusals of the engine in internal/service/security/authz.
// The range was allocated in the service layer (LL = 3) and is declared here,
// beside the domain's verdicts, since ADR 0160: LL records the layer that
// allocated a range, not the directory its declaration lives in, so the values
// never change.

// CodeGrantInvalid identifies an RBAC grant table refused at construction: no
// roles attribute named, no grants, an unnamed role, a role that confers
// nothing, or a permission with an empty half.
const CodeGrantInvalid errs.Code = 0x00_03_38_01 // 0.3.56.1

// CodeRuleInvalid identifies an ABAC rule set refused at construction: no
// rules, an unnamed rule, an empty action or resource, a nil condition, or an
// effect that is neither Allow nor Deny.
const CodeRuleInvalid errs.Code = 0x00_03_38_02 // 0.3.56.2

// CodeConditionInvalid identifies a condition constructor refused at
// construction: an empty attribute name, an empty set member, a nil inner
// condition, or a combinator over no conditions at all.
const CodeConditionInvalid errs.Code = 0x00_03_38_03 // 0.3.56.3
