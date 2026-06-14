# internal/service/proc/rlimit/

## Purpose

Applies per-process **setrlimit(2)** / **prlimit64(2)** resource ceilings for the
OS process-supervision domain (ADR 0016). It maps the abstract
`core/proc.Resource` enum to the platform `RLIMIT_*` constant and issues the
syscall, wrapping failures in the central `core/proc` sentinels. **Stdlib-only**
(`syscall`, `unsafe`) plus `internal/kernel/errs` — no `golang.org/x/sys`.

## Contents

| File | Role |
|---|---|
| `rlimit.go` | platform-neutral surface: `Apply`, `PrepareSysProcAttr` delegating to the build-tagged impls |
| `rlimit_linux.go` | Linux impl: `Resource → RLIMIT_*` table, `setrlimit`/`prlimit64`, error wrapping |
| `rlimit_other.go` | `!linux` stub: every entry point returns `UnsupportedPlatform` |

No `codes.go` / `errors.go` — every error is a `core/proc` sentinel
(`UnknownResource`, `RlimitFailed`, `UnsupportedPlatform`); this package mints no
codes and never calls `errs.Define`.

## Behaviour

- **Apply(pid, limits)** — `pid == 0` or `pid == getpid()` uses `setrlimit(2)`
  (calling process only); any other pid uses `prlimit64(2)` via
  `Syscall6(SYS_PRLIMIT64, ...)`, which needs `CAP_SYS_RESOURCE`. An unmapped
  `Resource` (including the zero value) returns `UnknownResource`; a syscall
  failure returns `RlimitFailed` wrapping the errno, annotated with `pid` +
  `resource`.
- **PrepareSysProcAttr(limits)** — validates the limit set with **no syscall**
  and returns the same typed errors `Apply` would. It exists because Go's
  `os/exec.SysProcAttr` carries **no** rlimit field: limits cannot be applied
  declaratively before exec. The honest model is post-fork application (a pid-0
  `Apply` in the child, or `prlimit64` against the spawned pid from the
  supervisor). Call it at `Spec`-construction time to fail fast.

## Platform notes

- Linux only. `RLIMIT_NPROC` (6) and `RLIMIT_MEMLOCK` (8) are **absent** from
  Go's `syscall` package and are restated as constants from the kernel generic
  ABI (`asm-generic/resource.h`). The other seven come from `syscall`.
- Off Linux the stub returns `UnsupportedPlatform` — it never acts, never
  panics. Every GOOS compiles.

## Do NOT

- Call `errs.Define` — all 22 codes live in `core/proc`; adding one breaks the
  AST audit. Restate the sentinel fields in `errs.Wrap` instead.
- Pretend `SysProcAttr` can carry rlimits; it cannot. Keep the post-fork model.

## Verification

```sh
bazel test --config=race //internal/service/proc/rlimit:rlimit_test
# Fallback
go test -race ./internal/service/proc/rlimit/...
```

The acceptance test applies a lowered `RLIMIT_NOFILE` to the running process and
reads `/proc/self/limits` to assert the new soft ceiling is observable.
