# internal/service/gate/

## Purpose

The classifier behind `pkg/v1/gate` (ADR 0080). One exported function, and it
performs no effect.

`Decide` reads a policy, a command path and whatever the caller's verifier
returned, and says what the caller should do. The network access, the upgrade
and the process exit stay in the caller's control flow.

## Why-this-shape

- **The ORDER is the contract**, and every way of getting it wrong compiles.
  - **Exemption is read FIRST, and `verify` is not CALLED when it holds.**
    `completion` runs from a shell hook where a network round trip is hostile,
    and `license status` must work on exactly the machine whose licence is
    broken. Removing the check fails four cases, one of them on the call count:
    `Decide() called the verifier 1 time(s) on an exempt invocation, want 0`.
  - **The floor is read BEFORE the refusal is propagated.** An out-of-date
    binary must be told to upgrade whether or not its entitlement is also in
    order: the action is the same either way, and reporting "your licence is
    broken" to somebody whose answer is "run upgrade" sends them to the wrong
    place.
- **A nil policy refuses everything, including a verification that passed** —
  and does not call `verify` at all. The documentation said so and the first
  version of the code did not: a nil policy fell through to the ordinary-path
  branch and allowed. Review caught it. No verification can change a refusal
  that is already settled, so paying for one would be the same waste the
  exemption check avoids.
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
