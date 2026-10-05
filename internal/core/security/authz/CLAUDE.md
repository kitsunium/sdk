<!-- updated: 2026-10-03T07:14:15Z -->
# internal/core/security/authz/

## Purpose

Declares the **authorization port**: `Policy` (may this subject do this to
that), the three-valued `Decision` it answers with, the immutable
`RequestValue` that asks, the typed `AttrValue` facts attached to it, and the
`Condition` an attribute rule is written as. The 24th core sibling, admitted by
**ADR 0057**. The RBAC and ABAC evaluators, the deny-overrides combiner, the
closure and the built-in conditions live in `internal/service/security/authz`.

Code ranges: `0.2.26.*` — the port's four verdicts (ADR 0057) — and
`0.3.56.*` — the engine's three construction refusals, allocated in the
service layer and declared here since ADR 0160, with their values unchanged.
Every code of the domain is in this package; the engine declares none.

## Contents

| File | Surface |
|---|---|
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `AttrKind`, `AttrValue`, `Policy`, `Condition`, `Decision` and `RequestValue` — each struct with every field, unexported ones included; `AttrValue.StringsValue`, `AttrValue.Contains`, `Decision.Granted`, `Decision.Valid`, `RequestValue.AttrCount`, `AttrString`, `AttrInt64`, `AttrBool`, `AttrStrings` and `NewRequestValue`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| `decision.go` | `Decision` + `Abstain` / `Allow` / `Deny` + `Granted` / `Valid` / `String` |
| `request_value.go` | `RequestValue` + `NewRequestValue` + `Subject` / `Action` / `Resource` / `Attr` / `AttrCount` |
| `attr_value.go` | `AttrKind` (`KindInvalid` / `KindString` / `KindInt64` / `KindBool` / `KindStrings`) + `AttrValue` + `AttrString` / `AttrInt64` / `AttrBool` / `AttrStrings` + `Key` / `Kind`, the `(value, ok)` accessors and `Contains` |
| `codes_gen.go` | `Code*` constants — ranges 0.2.26.* (verdicts) and 0.3.56.* (the engine's construction refusals); `PermissionDenied` / `AttributeMissing` / `AttributeKindMismatch` / `PolicyMisconfigured` — the verdicts — and `GrantInvalid` / `RuleInvalid` / `ConditionInvalid` — the engine's construction refusals (`errs.Define`) — written by kit gen from `design/security/authz.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `httpForbidden`, `exitConfig` |

## The frontier

The SDK ships the **evaluation**. Everything below the line is the framework's,
because it needs to know what a request, a route and a user account are — and
this package deliberately does not.

| The SDK ships | The framework ships |
|---|---|
| `Policy` / `Decision` / `Condition` | A firewall, and the middleware that runs one |
| The RBAC and ABAC evaluators | Voters wired onto routes or controllers |
| The deny-overrides combiner | Which policy applies to which endpoint |
| `Check`, and its one refusal | What a denied request looks like on the wire (redirect, 403 page, problem+json) |
| Byte equality on action and resource | A resource hierarchy, a naming convention, a URN grammar |
| A grant table the caller supplies | **What a role is called, and who holds one** |
| The three-valued verdict | A login flow, a session cookie, a token issuer |

"Who is an admin" is the line. It is the application's vocabulary, it changes
per deployment, and every library that tried to own it grew a policy DSL to
express what it could not name — see ADR 0057 §D1.

## Conventions

- **No registry.** The set of evaluators is closed and composition is by
  function, not by name — the `proc` (ADR 0016), `resilience` (ADR 0026), `net`
  (ADR 0029), `scheduler` (ADR 0041), `token` (ADR 0042) and `session`
  (ADR 0045) precedents. A registry keyed on a configuration string would also
  let a typo silently swap a policy, which in this domain means silently
  swapping who gets in.
- **Both ports are FUNC types, not interfaces.** ADR 0039's rule is that a
  published port must not grow a method, because `pkg/v1/security/authz` aliases them
  and Go interfaces are structural. A func type satisfies that rule
  *structurally*. `TestPortsAreFunctionsNotInterfaces` is the executable guard.
- **Three states, and the zero one is `Abstain`.** ADR 0031 on a FUNC port:
  there is no constructor to refuse in, so the zero value has to be the safe
  one. A policy that forgets to set its result says "no opinion" — it does not
  authorize (the hole) and does not veto every sibling policy (the outage).
- **Read a verdict with `Granted()`, never `!= Deny`.** The second is true for
  `Abstain`, so it authorizes every request no policy recognised. `Granted` is
  the whole reason the method exists.
- **A `Decision` outside the three is `Deny`.** `Valid()` reports it and
  `String()` renders `"invalid"`, so a corrupt value can never appear in a log
  as if it were a verdict.
- **Absence and kind mismatch are not `false`.** `Attr` returns `(AttrValue,
  bool)`; every `AttrValue` accessor returns `(value, ok)`. A caller that
  ignores the second half gets a zero, which is why the service-layer
  conditions never ignore it.
- **`NewRequestValue` drops an unusable attribute rather than storing it.** An
  attribute with an empty key or `KindInvalid` becomes ABSENT, so "found but
  unusable" is a state no rule has to handle — and absence already refuses.
- **A set attribute is cloned in and cloned out.** One `RequestValue` is handed
  to every policy in a composition, on any goroutine; `Contains` scans in place
  so the hot path never pays for the clone.
- **Every verdict carries the same `Public`.** "Access to the requested
  resource is denied", four times, with four distinct `Private` messages.
  `TestEveryRefusalShowsTheSameSentence` and
  `TestPublicNamesNoAttributeRoleOrRule` are the executable guards.
- **All four are HTTP 403.** Including the two that are really evaluation
  faults: a 500 would tell a client to retry a request that will be refused
  identically forever, and tell an attacker which input the policy could not
  parse. A framework that prefers 404 overrides it at the edge.
- **The engine's three construction refusals are specific on purpose.**
  `GrantInvalid`, `RuleInvalid` and `ConditionInvalid` describe themselves in
  `Public`, carry no HTTP status and carry `EX_CONFIG` (78): they are raised by
  `internal/service/security/authz` while a policy is being ASSEMBLED, on a
  path where no request exists and no client is listening, so they never
  become a response. Their `Private` names the engine that raises them; they
  are declared here only so the domain's codes are in one place (ADR 0160).

## Relationship to `session` and `token`

`authz` answers "may this subject", never "who is this subject". The subject
string comes from `internal/core/security/session` (an opaque, revocable server-side
handle) or `internal/core/security/token` (a self-contained signed claim set). This
package imports neither: it takes the subject as given, so handing it one that
was never authenticated produces a perfectly valid decision about a principal
that does not exist. Authentication is the caller's step and stays visible as
one.

## Do NOT

- **Turn `Policy` or `Condition` into an interface, or add a method.**
  `pkg/v1/security/authz` aliases both, so the shape is published (ADR 0039).
- **Make `Allow` the zero `Decision`,** or add a fourth state that grants.
- **Add a wildcard, a separator or a matcher** to `RequestValue`'s three
  strings. Byte equality is the contract; a hierarchy is the caller's spelling
  and an instance rule is an ABAC condition.
- **Add a `Reason` or `Rule` field to the public side of a refusal.** The
  uniform sentence is the security property; the diagnosis goes in fields.
- **Put a concrete evaluator here.** RBAC, ABAC, the combiner and the built-in
  conditions are service concerns; the port knows about decisions and
  attributes, not about roles.
- **Add an expression grammar, a rule file format, or a relationship tuple.**
  ADR 0057 §D1 refuses all three by name.

## Verification

```
bazel test --config=race //internal/core/security/authz:authz_test
# OR
cd internal/core && GOWORK=off go test -race ./security/authz
```
