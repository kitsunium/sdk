# pkg/v1/vfs/

## Purpose

The public facade for the SDK's filesystem domain (**ADR 0056**): reading stays
`io/fs`, writing gets a contract, and publishing a file becomes one call that
either replaces it completely or changes nothing at all.

`README.md` is **generated** from the package doc comment in `vfs.go` by
`gomarkdoc` (rule 10 / ADR 0008). Edit the doc comment, then run
`make docs-readme`. Never hand-edit `README.md`.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `FS` | alias → `corevfs.FS` (= `io/fs.FS`) | reading is the stdlib's, unchanged |
| `WritableFS` | alias → `corevfs.WritableFS` | four write verbs on the embedded `FS`; frozen |
| `AtomicWriter` | alias → `corevfs.AtomicWriter` | ADR 0039 sibling, reached by type assertion |
| `FullFS` | alias → `corevfs.FullFS` | the union both constructors return |
| `NewOS(root)` | func | confined to one tree; refuses at construction |
| `NewMem()` | func | a filesystem in a map; takes no arguments, on purpose |
| `InvalidPath`, `InvalidPermission`, `PathEscaped`, `ReadFailed`, `WriteFailed`, `PublishFailed`, `NotRegularFile`, `DirectoryNotEmpty` | sentinels | re-exported from `internal/core/vfs` |
| `RootUnavailable`, `DirectorySyncFailed` | sentinels | re-exported from `internal/service/vfs` |

Every type is an **alias**, never a copy. A value crossing between `pkg/v1` and
`internal/*` therefore needs no conversion, and a stdlib walker takes an SDK
filesystem directly. `TestTheFacadePublishesAliasesAndNotCopies` pins it.

## Conventions

- **Ask for the narrowest thing you use.** `FullFS` is what the constructors
  return, but a parameter that only writes should say `WritableFS`, and one that
  only publishes should say `AtomicWriter`. `FullFS` exists so a caller wiring
  one of the SDK's own filesystems need not type-assert for the headline
  feature — not as the default parameter type.
- **A refusal carries both identities.** `errors.Is(err, vfs.ReadFailed)` and
  `errors.Is(err, fs.ErrNotExist)` both answer on the same error, which is what
  lets a consumer adopt this package one call at a time.
  `TestARefusalCarriesBothIdentities` is the guard.
- **`NewMem` is the double.** It answers the same typed refusals for the same
  inputs as `NewOS`, so a consumer's tests need no temporary directory. It says
  nothing about performance — see `internal/service/vfs/BENCH.md`.

## Do NOT

- **Do NOT hand-edit `README.md`.** It is generated; `make lint` blocks any
  commit where the file on disk differs from what `gomarkdoc` would produce.
- **Do NOT declare a new interface here.** The port lives in
  `internal/core/vfs`; this package re-exports it. A second declaration would
  break the alias property that makes stdlib walkers apply.
- **Do NOT add read helpers.** `fs.WalkDir`, `fs.Glob`, `fs.ReadFile`, `fs.Sub`
  and `fs.Stat` already work on any value from here.
- **Do NOT treat `NewMem` as a performance proxy for `NewOS`.** Publication is
  a map assignment in one and two device flushes in the other — roughly four
  orders of magnitude apart.

## Verification

```bash
cd pkg && GOWORK=off go test -race ./v1/vfs/...
# coverage: 100%
make docs-readme   # regenerates README.md from the package doc comment
```
