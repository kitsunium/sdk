// Package authz — range 0.3.56.* (ADR 0057 service/authz block).
package authz

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.56.0 - 0.3.56.255

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
