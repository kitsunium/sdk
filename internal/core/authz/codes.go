// Package authz — range 0.2.26.* (ADR 0057 core/authz block).
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
