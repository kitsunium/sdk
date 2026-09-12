# internal/core/gate/

## Purpose

The contract for deciding whether one invocation of a distributed binary may run
(ADR 0080): the policy value, the decision value, and the two enums whose zero
values are deliberately unclaimed. No ports — this domain has nothing to ask its
environment for.

Code range `0.2.36.*` (`0x00_02_24_*`), owned solely by this package. One code,
on purpose.

## Contents

| File | Role |
|---|---|
| `gate.go` | the package doc: what it decides and what it refuses to perform |
| `policy.go` | `PolicyValue`, `Exempt`, `Validate` |
| `decision.go` | `DecisionValue`, `Outcome` |
| `update_action.go` | `UpdateAction` — refuse, apply, warn |
| `codes.go` / `errors.go` | the range and its one refusal |

## Why-this-shape

- **Two exemption lists, not one.** A name-only match cannot express that
  `skill` runs unchecked while `skill install` does not — and that distinction
  is the difference between a gate and decoration. `ExemptExact` matches a whole
  path, `ExemptSubtree` a path and its descendants.
- **Descending is a prefix on a SEPARATOR boundary, never on bytes.** A byte
  prefix would exempt `licensed` from `license`'s subtree, which is a gated
  command silently running unchecked. `TestPolicyValue_Exempt` pins it and was
  seen failing with the boundary removed.
- **`RecoveryPaths` turns a comment into a refusal.** A policy that gates its
  own repair command locks a lapsed machine out with no path back short of
  reinstalling, and nothing about the policy LOOKS wrong — the exemption list is
  simply missing an entry. `Validate` refuses it.
- **An entry no path can equal is a lockout wearing a disguise.** `"license "`
  with a trailing space, or `"license\tcreate"` with a tab, matches nothing —
  so the exemption never fires and the command stays gated while the list LOOKS
  complete. `Validate` refuses any stray whitespace on all three lists, and the
  bare-root `""` is the one legitimate empty entry in `ExemptExact`.
- **An empty SUBTREE root is refused.** Its two available meanings are both
  wrong: covering only the bare invocation reads as a subtree that silently does
  not work, and covering everything would turn one stray entry into a gate that
  gates nothing.
- **`Validate` returns ONE error naming every fault**, not an `errors.Join`. A
  joined error has no single `Private` detail, so `errs.PrivateOf` reports the
  first fault's and a structured-logging caller would still fix them one
  start-up at a time — which is the thing "report every fault" promises not to
  do. The first version of this package joined; its own test caught it.
- **Both enums leave zero unclaimed** (ADR 0031). For `UpdateAction`, refusing
  and upgrading are opposite answers and neither is a guess this package may
  make on a vendor's behalf. For `Outcome`, a decision nobody made must not read
  as "allow".
- **`DecisionValue.Exempt` and `.FloorUnmet` are separate from `Outcome`**
  because they carry facts `Outcome` loses. Never-checked and
  checked-and-allowed are the same outcome and a very different fact — a daemon
  must not seed a watchdog from an exemption. And `UpdateWarn` allows the
  invocation while the floor is still unmet, which the caller has to report.

- **`Decide` takes a verifier, not a result.** An error parameter forces the
  caller to verify before the gate can say whether verification was needed, so
  every exempt command pays for a round trip it is exempt from. The function
  form makes "exemption first" structural, and the service suite counts the
  calls — counting is the only way to assert something did NOT happen.

## Do NOT

- Verify anything here, or upgrade anything, or end a process. Those are
  `entitlement`, `selfupdate`, and the caller's own `main`.
- Re-label `DecisionValue.Cause`. It is carried whole so `errors.Is` and
  `errs.CodeOf` answer through it; a gate that summarised it would force every
  consumer onto message text, and the distinction this domain must never blur is
  "cannot decide" against "decided no".
- Split the code range. Every policy fault has the same response — fix it and
  restart — so several codes would give a caller branches with one destination.

## Verification

```sh
bazel test --config=race //internal/core/gate:gate_test //internal/service/gate:gate_test
```
