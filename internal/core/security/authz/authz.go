package authz

import "context"

// Policy answers whether request is permitted. Implementations MUST be pure
// with respect to the decision — the same request yields the same decision —
// and safe for concurrent use, because one compiled Policy is shared by every
// goroutine serving a request.
//
// A Policy returns [Abstain] when it has nothing to say about this request.
// That is the common case for a policy that only knows about one action or one
// resource, and it is NOT a refusal: refusing is [Deny], and a policy that
// conflates the two silently vetoes every policy it is composed with.
//
// # A returned error is never an authorization
//
// The error return means "this policy could not be evaluated" — a required
// attribute was absent, an attribute was not the kind the rule compares, a
// backing store was unreachable. A caller MUST treat that as a refusal. The
// SDK's own combiner and closure do exactly that: an error is folded to [Deny]
// and the decision returned alongside it is ignored. A Policy that cannot
// decide has not permitted anything.
//
// Implementations SHOULD return [Deny] alongside a non-nil error rather than
// the zero [Abstain], so that a caller who reads only the decision — a mistake
// this contract cannot prevent — still fails closed.
type Policy func(ctx context.Context, request RequestValue) (Decision, error)

// Condition reports whether an attribute rule holds for request. It is the
// unit an ABAC rule is written in, and it is an ordinary Go predicate rather
// than a sentence in a policy language: the caller already has a language, and
// a second one would have to grow comparison, negation, arithmetic and short
// circuits before it could express what an `if` expresses today.
//
// Implementations MUST be pure and safe for concurrent use, and MUST NOT
// perform I/O — a Condition is evaluated on the request path, possibly several
// times per request, with no context to cancel it. A rule that needs a lookup
// is a [Policy], which has one.
//
// # false and "cannot tell" are different answers
//
// Returning (false, nil) means the condition was evaluated and does not hold;
// the rule carrying it declines to fire, and the decision is left to the other
// policies. Returning a non-nil error means the condition could NOT be
// evaluated — most often because the request does not carry the attribute the
// rule names ([AttributeMissing]), or carries it as another kind
// ([AttributeKindMismatch]).
//
// Those two MUST NOT be collapsed. An absent attribute compared to a wanted
// value is not a comparison that failed: it is a comparison that never
// happened, and reporting it as false would let a request carrying no
// attributes at all slip past every "deny if X" rule in the system. The
// built-in conditions in internal/service/security/authz report absence as an error,
// and every combinator over them treats an unevaluable branch as absorbing.
type Condition func(request RequestValue) (bool, error)
