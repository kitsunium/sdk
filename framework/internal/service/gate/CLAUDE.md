<!-- updated: 2026-10-05T00:00:00Z -->
# framework/internal/service/gate/

## Purpose

The classifier behind `framework/gate` (ADR 0080). One exported function, and it
performs no effect.

`Decide` reads a policy, a command path and whatever the caller's verifier
returned, and says what the caller should do. The network access, the upgrade
and the process exit stay in the caller's control flow.

It was the SDK library's `internal/service/gate` until ADR 0158 made it the
framework's. It declares no code of its own: every refusal it carries is the
core gate's `0.2.36.*`, or whatever the verifier returned, whole.

## How it works

This prose was the package's hand-written README. The README is now
written by `tools/genindex` from `docs/api`, as every framework package's is
(framework/CLAUDE.md rule 3), so it lives here.

### The order

1. **Exempt?** Read first, and `verify` is **not called** when it holds — an
   exempt command must not pay for a verification it is exempt from.
2. **Clean verification?** Allow.
3. **Version floor?** Apply the policy's `UpdateAction` — refuse, ask the caller
   to upgrade, or allow with `FloorUnmet` set.
4. **Anything else** is a refusal, with the cause carried whole.

Step 1 before step 4 is what keeps the command that repairs an entitlement
reachable when the entitlement is what is broken. Step 3 before step 4 is what
sends an operator to `upgrade` rather than to a licence they cannot fix.

### What it never does

Verify, upgrade, or end a process. A library that decides on your behalf whether
the process should still be alive makes the decision untestable and skips every
deferred function.

## Why-this-shape

- **The ORDER is the contract**, and every way of getting it wrong compiles.
  - **Exemption is read FIRST, and `verify` is not CALLED when it holds.**
    `completion` runs from a shell hook where a network round trip is hostile,
    and `license status` must work on exactly the machine whose licence is
    broken. Removing the check fails four cases, three of them on the call count:
    `Decide() called the verifier 1 time(s) when it should not have, want 0`.
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
bazel test --config=race //framework/internal/service/gate:gate_test
(cd framework && GOWORK=off go test -race ./internal/service/gate/...)
```
