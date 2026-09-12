# pkg/v1/git/

## Purpose

Thin **public facade** over `internal/service/vcs/git` (ADR 0076): what a branch
changed, by shelling out to the git binary. Consumers import this package; the
service and `core/vcs` stay internal.

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

`Config` and `Include` alias the **service**, not core, and ADR 0074 is why: they
are meaningful to exactly one engine. `ChangedSet`, `LineRange` and `Resolution`
alias **core**, because a second implementation of the port would have to speak
them.

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
