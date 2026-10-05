# framework/internal/core/git/

## Purpose

The version-control contract (ADR 0076): what "the set a branch changed" IS, and
what a resolution of it reports. Interfaces and immutable values only — the
implementation shells out to git and lives in `framework/internal/service/git`.

It was the SDK library's `internal/core/vcs` until ADR 0158 moved the domain into
the framework; there it is named `git`, as its public package always was, and
the importers spell it `coregit`.

## Contents

| File | Role |
|---|---|
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `ChangedSet`, `ResolutionValue` and `LineRangeValue` — each struct with every field, unexported ones included; `ResolutionValue.Degraded` and `LineRangeValue.Contains`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| `git.go` | the `ChangedSet` port, FROZEN at four methods, and `ResolutionValue` + `Degraded` |
| `line_range.go` | `LineRangeValue` + its inclusive `Contains` |
| `codes_gen.go` | the `0.2.33.*` range and its three sentinels — written by kit gen from `design/framework/git.yaml` (ADR 0164) |

Code range `0.2.33.*` (`0x00_02_21_*`), owned solely by this package. It is a
layer-2 value although the package is the framework's: a code keeps its value
when it moves (ADR 0160). Imports: the standard library and
`internal/kernel/errs`, nothing else (framework/CLAUDE.md rule 1).

## Why-this-shape

- **The domain is thin on purpose.** It names a changed set and the outcome of
  computing one. It does NOT model repositories, commits, refs or history: there
  is exactly one implementation, and a contract broader than it would describe
  nothing real. ADR 0076 §Alternatives records what a fuller VCS abstraction
  would have cost. When a caller needed a working tree's head commit (ADR 0100)
  it became a query of the ENGINE — `service/git.Head` and its
  `HeadValue` — and this package did not grow: a second implementation of
  `ChangedSet` would have no reason to produce it (ADR 0074).
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
- **Matching is lexical, with one exception, and the exception is the root.** A
  path is compared after `filepath.Clean`, which resolves no symbolic link, so
  two spellings of one file do not meet. That would be a footnote if a caller
  chose every spelling it uses — but it does not choose the repository root: a
  backend that canonicalises it hands back a set keyed under a path the caller
  never writes, and then EVERY query answers false while `Degraded` says the
  resolution succeeded. So the root the caller named is accepted alongside the
  backend's own, and only the root. An indirection elsewhere in a queried path
  still does not match, and the port comment says which is which.
- **No registry.** Its key would be a VCS name, and resolving one from a config
  string would let a typo swap the implementation with every call still
  succeeding — the argument ADR 0052 makes for `lock`.

## Do NOT

- Add a concrete `ChangedSet` here. It is a stateful container with maps and a
  constructor; the layer's own Do-NOT list excludes those. It lives in the
  service.
- Model git in this package. Its NAME follows its one implementation (ADR 0158
  renamed `vcs` to `git`), but the port stays about a changed set: no refs, no
  commits, no history, no porcelain — a second implementation must still be able
  to satisfy it.

## Verification

```sh
bazel test --config=race //framework/internal/service/git:git_test
(cd framework && GOWORK=off go test -race ./internal/core/git/... ./internal/service/git/...)
```
