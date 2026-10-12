# pkg/v1/proc/buildinfo/

## Purpose

Thin **public facade** over `internal/service/proc/buildinfo` (ADR 0100,
ADR 0172): what the running binary was built from — the main module with its
version-control stamp, and every dependency through its replacement, a
release, a commit and a local directory told apart.

`pkg/v1/proc/process` re-exports the same aliases and functions beside
spawning children and `Self`. This package is the reading ALONE, and that is
its only reason to exist: importing `process` initialises `runtime/metrics`
(for `Self`) and the spawn engine, which a program that only asks what it was
built from — the framework's `kit`, in every product — would pay for at every
start and never use (ADR 0172).

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `BuildInfo` | type alias | `= svcbuild.BuildValue`, the same type as `process.BuildInfo` |
| `Module` | type alias | `= svcbuild.ModuleValue`, the same type as `process.Module` |
| `Build()` | func | the running binary; `false` without build information |
| `ParseBuild(info)` | func | a `*debug.BuildInfo` from elsewhere, read the same way |

- **README is generated.** `README.md` is written by `tools/genindex` from the
  committed `docs/api` (`make docs-readme`, ADR 0167). The package comment is in
  `doc.go`, which kit writes from the design (`design/proc.yaml`).

## Do NOT

- Add `Self`, `Start` or anything else of `process` here: the point of the
  package is what it does NOT import.

## Verification

```sh
go test ./pkg/v1/proc/buildinfo/
bazel test //pkg/v1/proc/buildinfo:buildinfo_test
```
