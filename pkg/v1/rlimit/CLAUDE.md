# pkg/v1/rlimit/

## Purpose

Thin **public facade** over `internal/service/proc/rlimit` (ADR 0016): apply
per-process `setrlimit(2)` / `prlimit64(2)` resource ceilings. Consumers import
this package; the service and `core/proc` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Resource` | type alias | `= coreproc.Resource` — never a new named type |
| `Limit` | type alias | `= coreproc.LimitValue` (soft/hard pair) |
| `Resource*` | const | re-exported enum values, typed `Resource` |
| `LimitInfinity` | const | `uint64` "no limit" (RLIM_INFINITY) |
| `Apply(pid, limits)` | func | delegates verbatim to the service |
| `PrepareSysProcAttr(limits)` | func | no-syscall validator; delegates |

All types are **aliases**, so values cross the facade boundary without
conversion. The functions are one-line delegations — no logic lives here.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`rlimit.go` (ADR 0008). Edit the doc comment, then run `make docs-readme` (or the
`//go:generate` line). Do **not** hand-edit `README.md`.

## Spec integration

Go's `os/exec.SysProcAttr` has no rlimit field. `PrepareSysProcAttr` validates a
limit set without acting; actual application is post-fork (`Apply(0, …)` in the
child) or via `prlimit64` against a spawned pid. The doc comment documents this
honestly.

## Verification

```sh
bazel test --config=race //pkg/v1/rlimit:rlimit_test
go test -race ./pkg/v1/rlimit/...
```
