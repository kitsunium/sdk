# pkg/v1/gate/

## Purpose

Thin **public facade** over `internal/service/gate` (ADR 0080): decide whether
one invocation of a distributed binary may run. Consumers import this package;
the service and `core/gate` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Policy` | type alias | `= coregate.PolicyValue` — exemptions, recovery paths, update action |
| `Decision` | type alias | `= coregate.DecisionValue` — outcome, cause, and two facts the outcome loses |
| `UpdateAction` | type alias | `UpdateRefuse` / `UpdateApply` / `UpdateWarn`; zero unclaimed |
| `Outcome` | type alias | `OutcomeAllow` / `OutcomeRefuse` / `OutcomeUpgrade`; zero unclaimed |
| `Decide` | func | the classifier; performs nothing, and calls your verifier at most once |
| `CodePolicyInvalid` | const | for `errs.HasCode` on a `Validate` refusal |

## It performs nothing, and that is the design

`Decide` does not verify — that is `pkg/v1/entitlement`. It does not upgrade —
that is `pkg/v1/selfupdate`. And it never ends your process.

```go
decision := gate.Decide(policy, invocation.Path[1:], func() error {
	_, err := service.Verify(time.Now())
	return err
})
switch decision.Outcome {
case gate.OutcomeAllow:
	// run it; decision.FloorUnmet may still want reporting
case gate.OutcomeUpgrade:
	// upgrade AT MOST ONCE, then re-run
default:
	fmt.Fprintln(os.Stderr, decision.Cause)
	os.Exit(errs.ExitCodeOf(decision.Cause))
}
```

`Path[1:]` because the gate wants the path RELATIVE to the root; `cli.Invocation`
includes the binary's own name first.

## The verifier is a function, and is not called when exempt

`Decide` takes `func() error`, not an `error`. An error parameter would force
you to verify **before** the gate can say whether verification was needed — so a
shell-completion hook and a `license status` on a broken machine would both pay
for a network round trip they are exempt from.

## Upgrade AT MOST ONCE

`OutcomeUpgrade` asks the caller to upgrade and re-run. A caller that loops on
it will loop forever against a floor no published release satisfies — a roster
asking for v9.9.9 while the mirror serves v1.5.10. The refusal in
`Decision.Cause` names the required version precisely so the caller can check
the new build actually clears the bar.

## The policy refuses to be unsafe

`Policy.Validate` reports every fault at once, and two of them are lockouts
rather than typos:

- **No exemption at all** — the binary cannot be repaired from inside itself.
- **A gated recovery command** — a machine whose entitlement lapsed has no path
  back short of reinstalling, and nothing about the policy looks wrong.
  `RecoveryPaths` is what turns that from a comment into a refusal.

## Two lists, because one cannot say it

`ExemptExact` matches a whole path; `ExemptSubtree` matches a path and its
descendants. A product where `skill` prints install guidance (unchecked) and
`skill install` does real work (checked) needs both — and `licensed` does not
inherit `license`'s exemption, because descending is a prefix on a separator
boundary rather than on bytes.

## README is generated

`README.md` comes from `gomarkdoc` (ADR 0008). Regenerate with
`make docs-readme` — which runs `cd pkg/v1 && go generate ./...`, not `gomarkdoc`
from the repository root. Do **not** hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/gate:gate_test
```
