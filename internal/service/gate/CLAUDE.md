# internal/service/gate/

## Purpose

The classifier behind `pkg/v1/gate` (ADR 0080). One exported function, and it
performs no effect.

`Decide` reads a policy, a command path and whatever the caller's verifier
returned, and says what the caller should do. The network access, the upgrade
and the process exit stay in the caller's control flow.

## Why-this-shape

- **The ORDER is the contract**, and every way of getting it wrong compiles.
  - **Exemption is read FIRST, before `verifyErr` is looked at.** A caller runs
    the verifier for its own reasons, but an exempt command must run whatever it
    said — or `license status` stops working on exactly the machine an operator
    is trying to diagnose. Removing that check fails three cases.
  - **The floor is read BEFORE the refusal is propagated.** An out-of-date
    binary must be told to upgrade whether or not its entitlement is also in
    order: the action is the same either way, and reporting "your licence is
    broken" to somebody whose answer is "run upgrade" sends them to the wrong
    place.
- **A nil policy exempts nothing and refuses.** That is the direction an absent
  decision must take, and it is the path that runs when a caller has configured
  nothing at all.
- **An unset `UpdateAction` refuses** rather than defaulting to one of the
  three. It is reachable only from a policy that never went through `Validate`,
  and refusing is the direction `Validate` would have taken at start-up.

## Do NOT

- Add an effect. The moment this package fetches, writes or exits, the decision
  stops being testable without one.
- Collapse `Exempt` into `Outcome`. They answer different questions and a
  caller needs both.

## Verification

```sh
bazel test --config=race //internal/service/gate:gate_test
```
