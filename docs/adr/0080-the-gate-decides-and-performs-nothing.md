# ADR 0080 — the gate decides, and performs nothing

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Related**: [ADR 0079](0079-the-entitlement-split-x-sys-was-never-in-the-mechanism.md) (the domain it gates on), [ADR 0077](0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md) (the upgrade it asks for), [ADR 0065](0065-sdk-cli-domain.md) (why nothing here ends a process), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (both enums leave zero unclaimed), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range), [ADR 0035](0035-pp-range-ownership-enforcement.md) (range ownership)

## Context

A product that verifies an entitlement at start-up has to answer three questions
before it does any work:

- Is THIS invocation subject to the check at all?
- The vendor mandates a newer build. Refuse, upgrade, or warn?
- When the answer is no, what should the process do?

`entitlement` (ADR 0079) answers *whether this machine is entitled*.
`selfupdate` (ADR 0077) *replaces the binary*. Neither owns the three questions
above, so every consumer answers them by hand, in a root command, in code
nobody revisits.

The worked example is `kodflow/ktn-linter`. Its `license_gate.go` carries a
hand-maintained exemption table, the decision to upgrade-and-re-exec, and the
mapping from refusal to exit status — around 500 lines, of which the policy is
a dozen and the rest is plumbing. The policy half is not linter-specific at all.

Two things in that table are worth lifting, because both are mistakes that
compile.

**A name-only match is too coarse.** `skill` on its own prints install guidance
and must not require a licence; `skill install` does real work and must. One
list cannot say both.

**The recovery hatch is easy to lose.** A policy that gates the very command an
operator runs to repair a lapsed entitlement locks the machine out with no path
back short of reinstalling the binary — and nothing about the policy *looks*
wrong. The exemption list is simply missing an entry.

## Decision

**A new domain, `gate`, that owns the policy and the decision and performs
nothing.**

```
internal/core/gate/       PolicyValue, DecisionValue, UpdateAction, Outcome, 1 code
internal/service/gate/    Decide() — one function, no effects
pkg/v1/gate/              the facade
```

### 1. It performs nothing, and that is not squeamishness

`Decide` does not verify, does not upgrade, and does not exit. It reads what the
caller's verifier returned and says what to do; the caller performs it.

This is ADR 0065's rule, applied one domain over. `cli` refuses
`flag.ExitOnError` because calling `os.Exit` from inside a library makes the
parse untestable, skips every deferred function, and decides on the caller's
behalf whether the process should still be alive. A gate that upgraded the
binary itself would do all three, on a path that touches the network and
replaces the running executable.

The cost is one `switch` at the call site. The benefit is that the decision is a
pure function of three inputs, so a product can test its own policy without a
network, a signing key or a subprocess.

`Decide` takes a **verifier function**, not a verification result, and that is
not a style choice. An error parameter forces the caller to verify BEFORE the
gate can say whether verification was needed — so `version`, `help` and
`completion` all pay for a network round trip they are exempt from, and
`completion` runs from a shell hook where one is actively hostile. The function
form makes "exemption first" structural rather than documentary: the verifier is
not called at all when the invocation is exempt, and the suite counts the calls,
because counting is the only way to assert that something did not happen.

It still does not verify. It calls back into a function the caller supplied, at
the moment the decision needs one.

### 2. Two exemption lists, and a separator boundary

`ExemptExact` matches a whole path. `ExemptSubtree` matches a path and every
command under it. That is the smallest pair that expresses "the parent is exempt
and its children are not", which is the case a single list gets wrong.

Descending is a prefix **on a separator boundary, never on bytes**: a byte
prefix exempts `licensed` from `license`'s subtree, which is a gated command
silently running unchecked. The suite pins it, and it was seen failing with the
boundary removed:

```
Exempt("licensed") = true, want false — descending is a prefix on a
SEPARATOR boundary, never on bytes
```

### 3. `RecoveryPaths` turns a comment into a refusal

The policy names the commands that repair a refused entitlement, and `Validate`
REFUSES a policy in which any of them is gated. So does a policy that exempts
nothing at all: such a binary cannot be repaired from inside itself.

And so does an entry no command path can ever equal. A path joins command names
with ONE space and a name contains no whitespace AT ALL, so `"license "` — a
trailing space — and `"license\tcreate"` — a tab — match nothing: the exemption
never fires and the command it was written for stays gated. That is the same
lockout as a missing entry wearing the disguise of a present one, and it is the
reason this check runs before anything else reads the lists. It applies to
`RecoveryPaths` too: an unreachable recovery path is a recovery path that does
not exist.

An empty SUBTREE root is refused for a related reason. It is not the bare root —
`ExemptExact` already says that — and it is not "everything" either. The two
available meanings are both wrong: covering only the bare invocation reads as a
subtree that silently does not work, and covering the whole tree would turn one
stray entry into a gate that gates nothing.

`Validate` returns **one** error naming every fault rather than an
`errors.Join` of several. That distinction is the difference between a promise
and a delivered one — a joined error has no single `Private` detail, so
`errs.PrivateOf` reports the first fault's and a structured-logging caller would
still fix them one start-up at a time. The first version of this package joined;
its own test caught it.

### 4. A nil policy refuses, including a verification that passed

The documentation said "a nil policy exempts nothing and refuses" and the code
allowed: a nil policy with a clean verification fell through to the
ordinary-path branch. Review caught it.

Refusing is the only safe answer to invent. The policy is what says which
commands may run at all, so without one there is no answer — and `verify` is not
called either, because no verification can change a refusal that is already
settled. Same reasoning as the exemption check: do not pay for an answer that
cannot matter.

### 5. Both enums leave zero unclaimed

`UpdateAction`'s zero is not a default because refusing and upgrading are
opposite answers: a binary distributed to machines an operator does not own must
not replace itself uninvited, and one distributed to a fleet that tracks the
floor must not stop working instead. `Outcome`'s zero is not a default because a
decision nobody made must not read as "allow". ADR 0031.

### 6. `DecisionValue` carries two facts `Outcome` loses

`Exempt` distinguishes never-checked from checked-and-allowed. They are the same
outcome and a very different fact: a daemon that seeds a watchdog from a
verification must not seed it from an exemption, because there is no grant, no
deadline and nothing to age against.

`FloorUnmet` survives `UpdateWarn`, which allows the invocation while the floor
is still unmet — something the caller has to report and `Outcome` alone cannot
say.

## Consequences

- A consumer expresses its gate policy as a value, tests it without a network,
  and gets a construction-time refusal for the two faults that are lockouts.
- `Decision.Cause` is the verifier's error, carried whole. `errors.Is` and
  `errs.CodeOf` answer through it exactly as they would on the verifier's own
  error, so the distinction between "cannot decide" and "decided no" survives
  the gate — which is the one this stack must never blur.
- The domain claims code range `0.2.36.*` (`0x00_02_24_*`) with **one** code.
  Every policy fault has the same response — fix it and restart — so several
  would give a caller branches with one destination.
- `internal/core/gate` declares no port. `internal/core/validation` is the
  existing precedent for a core domain of values and pure functions.

## Breaking changes

None. `gate` is new.

## Alternatives considered

- **Let `Decide` run the verifier and the upgrade.** It removes a `switch` from
  every consumer and puts a network call, a signature check and a process
  replacement inside a function whose whole value is being a pure decision. The
  first consumer wanting to log the attempt, or to refuse the upgrade on a
  read-only filesystem, would have to stop using it.
- **Put the exemption table in `entitlement`.** It is not about entitlement: a
  product could gate on a feature flag, a kill switch or a support contract with
  the same policy. Coupling them would also make `pkg/v1/entitlement` the place
  a caller looks for command paths.
- **One exemption list with a trailing wildcard** (`license/*`). It expresses
  the subtree but needs a second spelling for "the parent too", and it invents a
  pattern grammar — with escaping, precedence and an empty-match question — for
  a problem two lists answer exactly.
- **Return an `error` instead of a `DecisionValue`.** `OutcomeUpgrade` is not a
  failure and `UpdateWarn` allows the invocation, so two of the four answers
  would be an error the caller must not treat as one.

## Deferred

- **Re-exec after an upgrade.** `OutcomeUpgrade` asks the caller to upgrade and
  re-run, and re-running is `syscall.Exec` on unix and a different shape on
  Windows. It belongs with `selfupdate`'s replacement step, which already
  documents Windows as unsupported (ADR 0077 §Deferred), rather than here.
- **An exemption that depends on flags rather than the path.** `--help` on a
  gated command is arguably an exemption too. It needs the flag set, which drags
  `cli` into this domain's signature; the path-only form covers the cases the
  worked example had.

## References

- [ADR 0079](0079-the-entitlement-split-x-sys-was-never-in-the-mechanism.md) — the domain being gated on
- [ADR 0065](0065-sdk-cli-domain.md) — "nothing here can end your process", the rule this follows
- [ADR 0031](0031-policy-zero-values-are-never-inert.md) — why both enums refuse their zero
