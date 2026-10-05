<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/proc/rlimit/

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

`README.md` is written by `tools/genindex` from the committed `docs/api`
(`make docs-readme`, ADR 0167). Do **not** hand-edit `README.md`. The package
comment is in `doc.go`, which kit writes from the design (`design/proc.yaml`):
edit the design, run `kit gen`, then `make api` and `make docs-readme`.

## Spec integration

Go's `os/exec.SysProcAttr` has no rlimit field. `process.Start` applies a
`Spec.Rlimits` set itself, through a re-exec trampoline; this package is the
standalone primitive for the other cases. `PrepareSysProcAttr` validates a
limit set without acting; application is post-fork (`Apply(0, …)` in the
child, `setrlimit(2)` on every Unix) or via `prlimit64` against a spawned pid
(Linux only — another Unix answers `UnsupportedPlatform` for a foreign pid).
The doc comment documents this honestly.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/proc/rlimit:rlimit_test
go test -race ./pkg/v1/proc/rlimit/...
```
