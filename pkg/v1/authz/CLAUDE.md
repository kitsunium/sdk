# pkg/v1/authz/

## Purpose

Public facade for the SDK's **authorization** domain (ADR 0057): type aliases
onto `internal/core/authz` and `internal/service/authz`, plus thin delegating
constructors. It answers "may this subject do this to that" and nothing else.

`README.md` is **generated** from this package's doc comment by `gomarkdoc`
(rule 10 / ADR 0008). Consumer-facing prose belongs in `authz.go`'s package
comment; maintainer rationale stays here.

## Surface

| Kind | Names |
|---|---|
| Ports (aliases) | `Policy` · `Condition` |
| Values (aliases) | `Decision` · `Request` · `Attr` · `AttrKind` · `Permission` · `Grant` · `RBACConfig` · `Rule` |
| Decisions | `Abstain` (zero) · `Allow` · `Deny` |
| Attribute kinds | `KindInvalid` (zero) · `KindString` · `KindInt64` · `KindBool` · `KindStrings` |
| Build a question | `NewRequest` · `AttrString` · `AttrInt64` · `AttrBool` · `AttrStrings` |
| Build a policy | `NewRBAC` · `NewABAC` · `DenyOverrides` · `Must` |
| Build a condition | `AttrEquals` · `AttrIsTrue` · `AttrAtLeast` · `AttrContains` · `AttrMatchesSubject` · `Not` · `AllOf` · `AnyOf` · `MustCondition` |
| Ask | `Check` |
| Sentinels | `PermissionDenied` · `AttributeMissing` · `AttributeKindMismatch` · `PolicyMisconfigured` · `GrantInvalid` · `RuleInvalid` · `ConditionInvalid` |
| Codes | `CodePermissionDenied` · `CodeAttributeMissing` · `CodeAttributeKindMismatch` · `CodePolicyMisconfigured` · `CodeGrantInvalid` · `CodeRuleInvalid` · `CodeConditionInvalid` |

## Why this shape

- **Aliases, never new types.** A `Condition` a consumer writes by hand
  satisfies `Rule.When` with no adapter, and a `Request` crosses the boundary
  unconverted. `TestAliasesShareIdentityWithTheInternalTypes` pins it.
- **Both ports are FUNC types (ADR 0039).** A published interface cannot grow a
  method without breaking every downstream implementer at compile time; a func
  type cannot grow one at all. Nothing here may widen them, and nothing here
  may add a sibling that a caller would have to type-assert on the request
  path.
- **`Check` is the only verdict most callers need.** It flattens `Deny`,
  `Abstain` and "could not be evaluated" into one `PermissionDenied`, on
  purpose: the party on the other side of the response must not be able to tell
  them apart. A caller that needs the distinction for alerting calls the
  `Policy` directly and reads `(Decision, error)` — that escape hatch is
  documented rather than removed.
- **The codes are re-exported, the `Define` calls are not.** The constants here
  are cross-package selectors, so the ADR 0035 ownership audit correctly skips
  them: `pkg/v1/authz` owns no range.

## Do NOT

- **Render `errs.PrivateOf` or `errs.FieldsOf` on the wire.** They carry the
  subject, the action, the resource and the failing rule's code — the whole
  reason the public sentence says nothing.
- **Test a verdict with `!= Deny`.** It is true for `Abstain`, so it authorizes
  every request no policy recognised. `Decision.Granted()` exists for this.
- **Cache a decision.** The full check is ~210 ns and allocates nothing (see
  `internal/service/authz/BENCH.md`); a cache is how a revoked role keeps
  working for five minutes.
- **Call `Must` / `MustCondition` on data read at run time.** They panic. They
  are for package-level wiring, where a fault must stop the process at start-up.
- **Wire a firewall, a route table or a role convention here.** That is the
  framework's layer — see `internal/core/authz/CLAUDE.md` §The frontier.
- **Hand-edit `README.md`.** Run `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/authz:authz_test
# OR
cd pkg && GOWORK=off go test -race ./v1/authz
```
