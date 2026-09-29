<!-- updated: 2026-09-28T16:42:12Z -->
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

Go's `os/exec.SysProcAttr` has no rlimit field. `process.Start` applies a
`Spec.Rlimits` set itself, through a re-exec trampoline; this package is the
standalone primitive for the other cases. `PrepareSysProcAttr` validates a
limit set without acting; application is post-fork (`Apply(0, …)` in the
child, `setrlimit(2)` on every Unix) or via `prlimit64` against a spawned pid
(Linux only — another Unix answers `UnsupportedPlatform` for a foreign pid).
The doc comment documents this honestly.

## Verification

```sh
bazel test --config=race //pkg/v1/rlimit:rlimit_test
go test -race ./pkg/v1/rlimit/...
```
