# internal/core/vcs/

## Purpose

The version-control contract (ADR 0076): what "the set a branch changed" IS, and
what a resolution of it reports. Interfaces and immutable values only — the
implementation shells out to git and lives in `internal/service/vcs/git`.

## Contents

| File | Role |
|---|---|
| `vcs.go` | the `ChangedSet` port, FROZEN at four methods |
| `line_range.go` | `LineRangeValue` + its inclusive `Contains` |
| `resolution.go` | `ResolutionValue` + `Degraded` |
| `codes.go` / `errors.go` | the `0.2.33.*` range and its three sentinels |

Code range `0.2.33.*` (`0x00_02_21_*`), owned solely by this package.

## Why-this-shape

- **The domain is thin on purpose.** It names a changed set and the outcome of
  computing one. It does NOT model repositories, commits, refs or history: there
  is exactly one implementation, and a contract broader than it would describe
  nothing real. ADR 0076 §Alternatives records what a fuller VCS abstraction
  would have cost.
- **`ResolutionValue` is a value, not `(ChangedSet, error)`.** Degrading is not
  failing. A resolver that cannot be trusted must never answer with an empty
  set, because "nothing changed" and "I could not tell what changed" are
  opposite instructions to a caller — conflating them is how a scoped review
  silently passes on a branch it never looked at.
- **The zero `ResolutionValue` is neither readable shape**, and that is
  deliberate: nil `Set` AND `FullFallback` false is a combination no constructor
  mints, so a caller that forgot to check cannot read "nothing changed" out of a
  value nothing produced. `Degraded()` is the guard.
- **Three granularities, none implying another in the obvious direction.** A
  pure rename or a deletion touches a FILE and its DIRECTORY while contributing
  no line range, so `ContainsFile` is true where `ContainsLine` is false for
  every line of it. A caller that only looked at lines would silently ignore a
  deleted file.
- **`ContainsDir` is not recursive.** The parent of a touched directory is not
  itself touched. The port comment says so, because the other reading is the
  one a caller assumes.
- **No registry.** Its key would be a VCS name, and resolving one from a config
  string would let a typo swap the implementation with every call still
  succeeding — the argument ADR 0052 makes for `lock`.

## Do NOT

- Add a concrete `ChangedSet` here. It is a stateful container with maps and a
  constructor; the layer's own Do-NOT list excludes those. It lives in the
  service.
- Name git in this package. The implementation is git today; the contract must
  not be.

## Verification

```sh
bazel test --config=race //internal/service/vcs/git:git_test
```
