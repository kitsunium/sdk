# pkg/v1/git/

## Purpose

Thin **public facade** over `internal/service/vcs/git` (ADR 0076, ADR 0100): what
a branch changed and what a working tree is at, by shelling out to the git
binary. Consumers import this package; the service and `core/vcs` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `ChangedSet` | type alias | `= corevcs.ChangedSet` — the four-method port |
| `LineRange` | type alias | `= corevcs.LineRangeValue` |
| `Resolution` | type alias | `= corevcs.ResolutionValue` |
| `Config` | type alias | `= svcgit.Config` — one engine's construction parameters (ADR 0074) |
| `Include` | type alias | `= svcgit.IncludeFunc` — the caller's file policy |
| `Resolve(ctx, cfg)` | func | delegates verbatim |
| `GitDir(ctx, root)` | func | delegates verbatim |
| `ShowFile(ctx, root, sha, rel)` | func | delegates verbatim |
| `HeadState` | type alias | `= svcgit.HeadValue` — `Revision`, `Time`, `Modified` (ADR 0100) |
| `Head(ctx, dir)` | func | delegates verbatim — HEAD's commit, its committer date, tracked changes |

`Config` and `Include` alias the **service**, not core, and ADR 0074 is why: they
are meaningful to exactly one engine. `ChangedSet`, `LineRange` and `Resolution`
alias **core**, because a second implementation of the port would have to speak
them.

## Both spellings of the root answer

`Resolve` records every entry under the root you gave AND the one git
canonicalised it into, and nothing else — at build time, so a query costs what
it always did. Pointing `Config.Root` at a symbolic link used to record every
path under the link's target, so a non-degraded, non-empty set answered false to
every query — the silent under-report, reached through the one path a caller
does not choose. An indirection elsewhere in a queried path is still lexical.

## `Head` is the engine's value, not the port's

`HeadState` aliases the SERVICE (ADR 0074, ADR 0100): the vcs port answers what
a branch changed and models no commit, so nothing a second implementation of it
produces would be this. `Modified` counts tracked files only — an untracked file
does not make a tree modified, unlike Go's own `vcs.modified` stamp — and a
failed status is an error, never "clean". Nothing is cached, because modified
changes without a commit; a caller asking often keeps its own answer.

## `ShowFile` has two refusals

`CodePathAbsent` — the commit is readable and its tree holds nothing there.
`CodeCommandFailed` — everything else. Match with `errs.HasCode`; the first is
the one that lets a caller tell "deleted" from "emptied", and an empty file is a
successful read of `""`.

## Check Degraded before reading Set

`Resolve` returns no error. Everything that prevents a trustworthy answer
produces `FullFallback` and a `Reason`, never a partial or empty set. The zero
`Resolution` is neither shape, so a caller that skips the check cannot read
"nothing changed" out of a value nothing produced.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in `git.go`
(ADR 0008). Regenerate with `make docs-readme`, which runs `cd pkg/v1 && go
generate ./...` — **not** `gomarkdoc` from the repository root, which resolves
`--repository.path` relative to the working directory and emits every symbol
link with the path segment twice. Do **not** hand-edit `README.md`.

## Verification

```sh
bazel test --config=race //pkg/v1/git:git_test
go test -race ./pkg/v1/git/...
```
