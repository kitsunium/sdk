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
| `rlimit_linux.go` | Linux impl: full `Resource → RLIMIT_*` table (incl. `NPROC`/`MEMLOCK`), `setrlimit`/`prlimit64`, error wrapping |
| `rlimit_unix.go` | Darwin/BSD impl (`unix && !linux`): native `setrlimit(2)` on self; a foreign pid → `UnsupportedPlatform` (no portable `prlimit64`) |
| `rlimit_table_as.go` / `rlimit_table_openbsd.go` | per-platform `RLIMIT_AS` split — present everywhere except OpenBSD (absent from its ABI → `UnknownResource`) |
| `rlimit_value_signed.go` / `rlimit_value_default.go` | `syscall.Rlimit` constructor: `int64` fields on FreeBSD/DragonFly, `uint64` elsewhere |
| `rlimit_other.go` | `!unix` stub (Windows, plan9, js/wasm): every entry point returns `UnsupportedPlatform` |

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

- **Native on every Unix target** (linux, darwin, freebsd, netbsd, openbsd,
  dragonfly). `setrlimit(2)` on the calling process is the shared mechanic; a
  lowered ceiling is observable via `getrlimit(2)` (`rlimit_unix_test.go`) on all
  of them, and via `/proc/self/limits` additionally on Linux.
- **Linux-only extras.** `RLIMIT_NPROC` (6) and `RLIMIT_MEMLOCK` (8) are absent
  from Go's `syscall` package and are restated from the kernel generic ABI
  (`asm-generic/resource.h`); off Linux they live in `golang.org/x/sys` (banned),
  so those two resources stay unmapped → `UnknownResource`. The Linux-only
  `prlimit64(2)` makes a **foreign pid** settable on Linux; off Linux a foreign
  pid yields `UnsupportedPlatform` (self-pid still works).
- **OpenBSD.** No `RLIMIT_AS` in its ABI → `ResourceAS` unmapped → `UnknownResource`.
- **Non-Unix** (Windows, plan9, js/wasm): the `!unix` stub returns
  `UnsupportedPlatform` — never acts, never panics. Every GOOS compiles.

## Performance — see `BENCH.md`

`PrepareSysProcAttr` is **9.6 ns for a nil limit set**, 74 ns for one resource and
142 ns for four, with **zero allocations at any size** — which is what makes the
"call it at `Spec`-construction time to fail fast" advice above free to follow
unconditionally. The refusal path is *cheaper* than the success path (67 ns), so
a caller's input cannot inflate it.

`Apply` is **79 % kernel**: 353 ns for `setrlimit(2)` on self, of which 278 ns is
the syscall. `prlimit64(2)` against a foreign pid is **1.7×** that (545 ns) — the
kernel finding and locking another task. Both are allocation-free, and 545 ns
against the ~626 µs a spawn costs is 0.09 %.

Measured correction to the note above: `prlimit64` does **not** require
`CAP_SYS_RESOURCE` when the caller's real/effective/saved uids match the target's
(`prlimit(2)`). `BenchmarkApply_ForeignPID` applies limits to its own child and
succeeds unprivileged; the capability is the sufficient condition, not the
necessary one.

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
