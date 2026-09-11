// Package authz declares the SDK's authorization port: the [Policy] that
// answers "may this subject perform this action on this resource", the
// three-valued [Decision] it answers with, the [RequestValue] that carries the
// question, and the [Condition] an attribute rule is written as. A core
// sibling admitted by ADR 0057.
//
// The port is a FUNCTION type rather than an interface — the shape
// internal/core/CLAUDE.md already admits for resilience.Operation,
// scheduler.Job, lifecycle.Start and validation.Constraint. ADR 0039's rule is
// that a published port must not grow a method, because pkg/v1 aliases publish
// the shape and Go interfaces are structural; a func type satisfies that rule
// structurally, since it cannot grow a method at all.
//
// # The SDK ships the evaluation, not the vocabulary
//
// "Who is an admin", "what a role is called", "which routes are protected" and
// "what happens to a denied request" are questions only the application can
// answer, and every library in this domain that tried to answer them grew a
// policy language to do it. This package therefore ships NO policy DSL, no
// expression parser, no role catalogue and no relationship tuple store: a
// [Condition] is an ordinary Go func, a grant table is data the caller writes,
// and composition is function composition. The engine evaluates; the
// vocabulary is the caller's. See ADR 0057 §D1.
//
// # It answers "may this", never "who is this"
//
// Identity arrives from internal/core/session (a server-side session, opaque
// and revocable) or internal/core/token (a self-contained signed claim set).
// This package takes the SUBJECT those produce and nothing else — it
// authenticates nobody, mints nothing, and reads no header. Handing it a
// subject the caller has not authenticated produces a perfectly valid decision
// about a principal that does not exist.
//
// # Three states, and the default is refusal
//
// A [Decision] is [Allow], [Deny] or [Abstain]. Abstention is a real answer —
// "this policy has no opinion about this request" — and it is deliberately not
// spelled as either of the other two. An evaluator that returned Deny where it
// merely had no grant would veto every other policy composed with it; one that
// returned Allow would authorize by silence. Both mistakes are unwritable here
// because the third state exists.
//
// Nothing in this package converts an abstention into a grant. The closure —
// the single place where "nobody said Allow" becomes a refusal — lives in
// internal/service/authz.Check, and it closes toward Deny. See ADR 0057 §D3.
//
// The concrete evaluators (RBAC over a grant table, ABAC over conditions), the
// deny-overrides combiner and the built-in conditions live in
// internal/service/authz; this package owns the contract, the domain values
// and the typed sentinels.
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
// built-in conditions in internal/service/authz report absence as an error,
// and every combinator over them treats an unevaluable branch as absorbing.
type Condition func(request RequestValue) (bool, error)
