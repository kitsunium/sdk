# gate (internal/service/gate)

Decides whether one invocation of a distributed binary may run. Internal service
implementation behind the public `pkg/v1/gate` facade — consumers import the
facade, not this package.

## API

```go
func Decide(policy *coregate.PolicyValue, path []string, verify func() error) coregate.DecisionValue
```

That is the whole surface, and it performs nothing.

## The order

1. **Exempt?** Read first, and `verify` is **not called** when it holds — an
   exempt command must not pay for a verification it is exempt from.
2. **Clean verification?** Allow.
3. **Version floor?** Apply the policy's `UpdateAction` — refuse, ask the caller
   to upgrade, or allow with `FloorUnmet` set.
4. **Anything else** is a refusal, with the cause carried whole.

Step 1 before step 4 is what keeps the command that repairs an entitlement
reachable when the entitlement is what is broken. Step 3 before step 4 is what
sends an operator to `upgrade` rather than to a licence they cannot fix.

## What it never does

Verify, upgrade, or end a process. A library that decides on your behalf whether
the process should still be alive makes the decision untestable and skips every
deferred function.
