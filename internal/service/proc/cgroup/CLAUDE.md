# internal/service/proc/cgroup/

## Purpose

Creates and manages **cgroup v2** control groups to confine a process tree's
memory / CPU / pids / IO, for the OS process-supervision domain (ADR 0016). It
implements the `core/proc.Group` port against the unified cgroup v2 hierarchy at
`/sys/fs/cgroup`. **Stdlib-only** (`os`, `path/filepath`, `strconv`, `errors`,
`syscall`) plus `internal/kernel/errs` — no `golang.org/x/sys`.

## Contents

| File | Role |
|---|---|
| `cgroup.go` | platform-neutral surface: `Available`, `Create` delegating to the build-tagged impls |
| `option.go` | `Option` functional-option type, `WithRoot`, `groupConfig` accumulator |
| `cgroup_linux.go` | Linux impl: `controlGroup` (satisfies `core/proc.Group`), controller writes, mkdir/rmdir |
| `cgroup_other.go` | `!linux` stub: `Available() == false`, `Create` returns `UnsupportedPlatform` |

No `codes.go` / `errors.go` — every error is a `core/proc` sentinel
(`CgroupUnavailable`, `CgroupCreateFailed`, `CgroupWriteFailed`,
`CgroupDeleteFailed`, `UnsupportedPlatform`); this package mints no codes.

## Behaviour

- **Available()** — honest delegation probe: the `cgroup.controllers` marker
  must exist under the mount **and** a throwaway sub-directory must be creatable
  (a read-only root, the common unprivileged-container case, reports `false`).
- **Create(name, opts...)** — `mkdir` a sub-group under the configured root
  (default `/sys/fs/cgroup`, override via `WithRoot` for a delegated sub-tree).
  An absent hierarchy or a delegation denial (EACCES/EPERM/**EROFS**) →
  `CgroupUnavailable`; any other `mkdir` fault → `CgroupCreateFailed`.
- **Set\*Max** — write the cgroup v2 interface file: `memory.max`, `cpu.max`
  (`"quota period"`), `pids.max`, one `io.max` line verbatim. A **negative**
  numeric value writes the literal `"max"` (no limit). Failure →
  `CgroupWriteFailed`.
- **Add(pid)** — write `pid` to `cgroup.procs`. Failure → `CgroupWriteFailed`.
- **Kill()** — write `"1"` to `cgroup.kill` (kernel ≥ 5.14): atomic, race-free
  SIGKILL of every member, **including `setsid` escapees** that `SignalGroup`
  misses. An absent feature file (older kernel) → `UnsupportedPlatform` so the
  caller can fall back; any other write fault → `CgroupWriteFailed`.
- **Freeze() / Thaw()** — write `"1"` / `"0"` to `cgroup.freeze` (kernel ≥ 5.2):
  quiesce/resume the whole tree (idempotent). Absent file → `UnsupportedPlatform`.
- **Delete()** — `rmdir` the group; the kernel refuses (EBUSY) until every
  process has left it → `CgroupDeleteFailed`.

## Platform notes

- cgroup v2 (unified hierarchy) is **Linux-only**. Off Linux the stub reports
  `Available() == false` and `Create` returns `UnsupportedPlatform`. Every GOOS
  compiles.
- Unprivileged hosts without cgroup delegation degrade gracefully to
  `CgroupUnavailable` — never a panic. The acceptance test gates the live
  confinement path behind `Available()` and `t.Skip`s with a reason otherwise,
  still asserting the typed-error contract.

## Do NOT

- Call `errs.Define` — all 22 codes live in `core/proc`. Restate the sentinel
  fields in `errs.Wrap` instead.
- Support cgroup v1 — only the unified v2 hierarchy is in scope (ADR 0016).
- Assume the top-level mount is writable; always probe (and prefer `WithRoot` to
  a delegated sub-tree).

## Verification

```sh
bazel test --config=race //internal/service/proc/cgroup:cgroup_test
# Fallback
go test -race ./internal/service/proc/cgroup/...
```
