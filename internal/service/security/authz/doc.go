// Package authz — hosts the attribute-based evaluator.
//
// Package authz implements the authorization port declared in
// internal/core/security/authz: the deny-overrides combiner, the closure that turns a
// three-valued [coreauthz.Decision] into one refusal, an RBAC evaluator over a
// grant table, an ABAC evaluator over conditions, and the built-in conditions
// those rules are written with. ADR 0057.
//
// # There is no policy language, and that is the decision
//
// Every general-purpose authorization library in this space eventually grows
// one: a string grammar for conditions, a matcher syntax for resources, a file
// format for the whole rule set. It is always justified as configurability and
// it always ends in the same place — a second, weaker language inside a
// program that already has one, with its own parser, its own precedence rules,
// its own injection surface and no type checker.
//
// This package refuses it. A condition is a Go func, a grant table is a Go
// slice, a resource is a string compared by equality. What a DSL would buy —
// changing a rule without recompiling — is a deployment property the caller
// can have by loading their own rule data through internal/service/app/config and
// building the policy from it, which keeps the parsing in their vocabulary
// rather than the SDK's. See ADR 0057 §D1.
//
// # No relationship model either
//
// There is no tuple store, no "object#relation@subject" grammar, no graph to
// walk. A Zanzibar-shaped model is a database with a consistency protocol,
// not a library function, and half of it is the storage this SDK does not have
// an opinion about. RBAC over the grants the caller supplies and ABAC over the
// attributes the caller attaches cover the two questions a request-path check
// can answer in microseconds without one.
//
// # The pieces
//
//   - [NewRBAC] — role-based grants. It answers Allow or Abstain and NEVER
//     Deny for an ungranted request; see its doc for why that distinction is
//     the difference between a composable evaluator and one that vetoes every
//     policy it is combined with.
//   - [NewABAC] — attribute rules, each with an explicit Allow or Deny effect
//     and a [coreauthz.Condition]. This is where an explicit refusal comes
//     from.
//   - [DenyOverrides] — the one combiner. Refusal wins, always.
//   - [Check] — the closure. It is the only place in the SDK where "nobody
//     said Allow" becomes an error, and it is the only place a caller should
//     get a verdict from unless they need the raw decision.
//
// Package authz — hosts the condition combinators.
//
// All three share one rule: an unevaluable branch is ABSORBING. If any branch
// reports that it could not be evaluated, the combinator reports the same and
// evaluates no answer from the others.
//
// That rule is what stops an absent attribute from being laundered into a
// grant. Negation is the sharpest case — Not over a condition that could not
// be evaluated must not become "true" — but AnyOf is the same defect one step
// further out: returning true because a sibling branch held would mean a
// request that omits an attribute satisfies a rule the attribute was there to
// constrain.
//
// Package authz — hosts DenyOverrides, the one combining algorithm.
//
// Package authz — hosts the built-in conditions an ABAC rule is written with.
//
// Every one of them reports an ABSENT or WRONG-KIND attribute as an error
// rather than as false. That is the rule the whole domain turns on: a
// comparison against an attribute the request does not carry is not a
// comparison that failed, it is one that never happened, and the two have
// opposite consequences the moment a rule is negated or combined.
//
// There is deliberately no Always condition. An unconditional rule is a grant
// with no reason, and the caller who wants one writes the predicate at the
// call site, where it appears in the diff and in review.
//
// Package authz — hosts GrantValue, one row of the role → permissions table.
//
// Package authz — hosts the role-based evaluator.
//
// Package authz — hosts RBACConfig, the arguments NewRBAC is built from.
//
// Package authz — hosts the construction-time refusals.
//
// Every check in this file rejects a policy that could never answer correctly,
// at the moment it is assembled rather than on the request that trips over it.
// A policy is built once at start-up and evaluated on every request; a fault
// found here costs one process start, and the same fault found at evaluation
// time costs one silent misbehaviour per request until somebody notices.
package authz
