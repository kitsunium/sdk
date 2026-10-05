// Package authz — hosts AttrValue, one typed fact about a request.
//
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
// Identity arrives from internal/core/security/session (a server-side session, opaque
// and revocable) or internal/core/security/token (a self-contained signed claim set).
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
// internal/service/security/authz.Check, and it closes toward Deny. See ADR 0057 §D3.
//
// The concrete evaluators (RBAC over a grant table, ABAC over conditions), the
// deny-overrides combiner and the built-in conditions live in
// internal/service/security/authz; this package owns the contract, the domain values
// and the typed sentinels.
//
// Package authz — ranges 0.2.26.* (the domain's verdicts) and 0.3.56.* (the
// engine's construction refusals) — ADR 0057, declared here since ADR 0160.
//
// Package authz — hosts Decision, the three-valued verdict a Policy returns.
//
// Package authz — declares the sentinel *errs.Error outcomes: the four
// verdicts of the port, and the three construction refusals of the engine in
// internal/service/security/authz (ADR 0160). Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
//
// Package authz — hosts RequestValue, the question a Policy answers.
package authz
