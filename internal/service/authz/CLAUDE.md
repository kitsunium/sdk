# internal/service/authz/

## Purpose

Implements `internal/core/authz`: the RBAC evaluator over a grant table, the
ABAC evaluator over conditions, the deny-overrides combiner, the closure that
turns a three-valued decision into one refusal, and the built-in conditions and
combinators rules are written with. **ADR 0057**.

Code range: `0.3.56.*` (construction refusals only — every evaluation outcome
emits a core sentinel from `0.2.26.*`).

## Contents

| File | Surface |
|---|---|
| `authz.go` | `Check` (the closure) · `Must` / `MustCondition` · the field-carrying `denied` helper |
| `combine.go` | `DenyOverrides` — the one combining algorithm |
| `rbac.go` | `PermissionValue` / `NewRBAC` + the inverted grant index |
| `grant_value.go` | `GrantValue` — one row of the role → permissions table |
| `rbac_config.go` | `RBACConfig` — the arguments `NewRBAC` is built from |
| `abac.go` | `RuleValue` / `NewABAC` + the rule fold |
| `conditions.go` | `AttrEquals` / `AttrIsTrue` / `AttrAtLeast` / `AttrContains` / `AttrMatchesSubject` |
| `combinators.go` | `Not` / `AllOf` / `AnyOf` |
| `validate.go` | every construction-time refusal, plus `kindName` |
| `codes.go` | `Code*` constants — range 0.3.56.* |
| `errors.go` | `GrantInvalid` / `RuleInvalid` / `ConditionInvalid` (`errs.Define`) |
| `BENCH.md` | the evaluation path is zero-allocation; the numbers and how to read them |

## The four rules this package exists to enforce

1. **The default is refusal.** `Check` is the single closure. `Allow` returns
   nil; `Deny`, `Abstain`, a nil policy, an out-of-contract decision and any
   evaluation error all return `PermissionDenied`. Nothing else in the SDK
   converts an abstention.
2. **A refusal wins a conflict.** `DenyOverrides` is the only combiner and it
   is not configurable. It evaluates EVERY member — only a `Deny` short-circuits
   — so it is commutative and associative, and a permutation of the policy list
   cannot change the verdict. `TestDenyBeatsAllowInEveryOrder` and
   `TestAllowDoesNotShortCircuit` are the executable guards.
3. **An abstention is distinguishable.** `NewRBAC` returns `Abstain` for an
   ungranted request, and `TestRBACAbstainsRatherThanDenying` composes it with
   a granting policy to prove it does not veto. An evaluator that refused where
   it merely had no grant would be absorbing, and the usual repair for that —
   a permissive combiner — reintroduces the hole one layer up.
4. **An absent attribute refuses.** Every built-in condition reports absence
   and kind mismatch as an error; `evaluateRules` folds an error to `Deny`
   whatever the rule's effect was; every combinator treats it as absorbing.

## Conventions

- **The grant index is inverted at construction.** `indexGrants` turns
  `role → permissions` into `permission → roles`, so an evaluation is one map
  lookup plus a scan of the roles that confer *that* permission — one or two in
  any realistic table — rather than a walk of the subject's roles against every
  grant. Growing the table grows the map, not the check.
- **The roles attribute is read BEFORE the grant table.** Checking it after
  would make the same broken request refuse on one path and abstain on another,
  so a producer bug would only surface on the requests that happened to match.
- **`RolesAttr` has no default and an empty one is refused.** Defaulting it to
  `"roles"` would be the SDK inventing the one piece of vocabulary it cannot
  know, and a caller who spells it `"groups"` would get an evaluator that finds
  no roles on every request — a silent, total, fail-closed outage.
- **A construction fault is refused at construction.** An empty grant table, an
  unnamed role, a rule with no name, a nil condition, an `Effect` left at its
  zero (which is `Abstain`) — all rejected by `validate.go`, once, at start-up.
  `Must` / `MustCondition` turn that into an init-time panic for wiring written
  in code; never call them on data read at run time.
- **`Contains` is used on the hot path, `StringsValue` is not.** The second
  clones — that clone is what keeps an attribute immutable when it leaves the
  domain — and the evaluation path deliberately never asks for one. This is why
  `BenchmarkCheckAllow` is 0 B/op.
- **The core sentinel is always the wrap ORIGIN.** `denied` and `attrFault`
  call `errs.Wrap(sentinel, WrapParams{}, fields...)`, the same shape
  `internal/service/session.wrapAs` uses. Wrapping the other way round would
  let an `AttributeMissing` become the code a framework routes on AND the
  message a client sees — the moment a refusal starts explaining which
  attribute to forge.
- **The three sentinels here are specific on purpose.** Unlike the core ones,
  `GrantInvalid` / `RuleInvalid` / `ConditionInvalid` describe themselves in
  `Public`: they are raised while a policy is being ASSEMBLED, on a path where
  no request exists and no client is listening, so they never become a
  response. All three carry `EX_CONFIG` (78).
- **There is no `Always` condition.** An unconditional rule is a grant with no
  reason; the caller who wants one writes the predicate at the call site, where
  it appears in the diff and in review.

## Do NOT

- **Add a second combining algorithm,** or a field that selects one.
  Permit-overrides, first-applicable and only-one-applicable are refused by
  name in ADR 0057 §D2. A break-glass path is a Go `if` around the composition,
  where the exception is visible.
- **Short-circuit `DenyOverrides` on an `Allow`.** That is first-applicable
  wearing this function's name, and it makes the answer order-dependent.
- **Make `NewRBAC` return `Deny` for an ungranted request.** It would veto
  every policy it is composed with.
- **Report an absent or wrong-kind attribute as `false`,** anywhere, for any
  reason — including inside a `Deny`-effect rule, where "it would have denied
  anyway" is right for the wrong reason and wrong the moment the rule is
  copied.
- **Invert or swallow an error in a combinator.** `Not` propagates it; `AllOf`
  and `AnyOf` evaluate every branch and return the first failure.
- **Skip a nil policy or a nil condition.** Skipping silently shrinks the
  policy set. Refuse.
- **Give a refusal a `Public` that says why.** The uniform sentence is the
  security property; fields carry the diagnosis.
- **Add a DSL, a rule file format, a wildcard matcher or a tuple store.**

## Verification

```
bazel test --config=race //internal/service/authz:authz_test
# OR
cd internal/service && GOWORK=off go test -race ./authz

# benchmarks (regenerates the numbers in BENCH.md)
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./authz/
```
