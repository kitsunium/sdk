# ADR 0018 — Cross-platform portability strategy

**Status**: Accepted
**Date**: 2026-06-14
**Deciders**: @kodflow
**Related**: ADR 0016 (process-supervision domain — the source of platform-specific calls),
ADR 0004 (Bazel visibility / Linux CI lane), ADR 0005 (dotted-quad error codes — the `UnsupportedPlatform` sentinel)

## Context

The SDK promises a **normed, performant toolbox** — every package behaves the
same way for every consumer. The `proc` domain (ADR 0016) is the first to break
the pure-Go uniformity that codec, crypto, logger, and transform enjoy: it makes
platform-specific **low-level calls** (`setrlimit`, `prctl(PR_SET_CHILD_SUBREAPER)`,
cgroup v2 writes, `wait4`, the sd_notify `AF_UNIX` datagram). These calls do not
exist — or behave differently — across the eight platforms the SDK targets:
`linux`, `darwin`, `windows`, `freebsd`, `openbsd`, `netbsd`, `dragonfly`.

"Works everywhere" is therefore **two independent bars**, and the SDK must clear
both:

1. **Build bar** — every package *compiles* on every `GOOS`. A platform-specific
   syscall or ABI constant must never silently drop a package from a target, and
   must never break the cross-compile.
2. **Runtime bar** — every package *behaves correctly* on the real kernel. A green
   `go build` proves the symbols resolve; it does not prove the syscall does what
   the contract says on that OS. Only execution on the real kernel proves that.

The working analysis behind this decision — the build matrix result, the
per-domain portability verdict, and the researched native equivalents — lived as
gitignored working notes (`.claude/contexts/cross-platform-portability-audit.md`).
This ADR makes that analysis the tracked, authoritative repo doc.

## Decision

### (a) Uniform contract — one typed answer everywhere

Where a platform has **no native mechanic** for a primitive, the package returns
the **same typed sentinel** — `UnsupportedPlatform` (`0.2.6.1`, declared centrally
in `internal/core/proc` per ADR 0016) — never a silent drop, never a build break,
never a panic. A consumer that calls `cgroup.SetMemoryMax` on OpenBSD gets the
exact same typed error it would get on Windows; the contract is uniform even where
the capability is not.

The abstract **port** (the interface + its immutable value types) stays
platform-neutral in `internal/core/proc`. Only the *implementation* is
build-tag-split, by this convention:

| Suffix | Selects |
|---|---|
| `_linux.go` | Linux-only mechanic (cgroup v2, `PR_SET_CHILD_SUBREAPER`) |
| `_unix.go` | the shared Unix mechanic (Linux + Darwin + all BSDs) |
| `_bsd.go` | a BSD-family mechanic that diverges from Linux/Darwin |
| `_windows.go` | the Windows mechanic |
| `_other.go` | the portable fallback — returns `UnsupportedPlatform`, compiles on every remaining `GOOS` |

Because the port is neutral and every primitive has an `_other.go` floor, the
package **always compiles**; only behaviour degrades.

### (b) Enforcement — a gate per bar

- **Build bar** is gated on every PR by `scripts/cross-platform-audit.sh` (local:
  cross-compiles **94 packages × 8 platforms** per module with `GOWORK=off`, exits
  non-zero on any failing cell) and its CI mirror `.github/workflows/cross-platform.yml`
  (a GitHub-hosted `GOOS/GOARCH` matrix, no infra dependency, `CGO_ENABLED=0`). A
  new platform-specific call that drops a package fails the matrix before merge.
- **Runtime bar** is validated by `.github/workflows/e2e-vm.yml` on **real OS
  kernels**. Bazel remains the canonical Linux gate (ADR 0004); the BSDs and
  Windows have no Bazel, so the platform-selected test suites run via
  `go test -c` (cross-compiled standalone binaries, no Go on the target) → SCP →
  SSH-execute on a persistent Proxmox VM (kodflow/labs infra: `[self-hosted, proxmox]`
  runners, ZFS-snapshot rollback per run). Only platform-sensitive `proc` packages
  run here; pure-Go packages behave identically everywhere and stay on the Linux lane.

### (c) The OpenBSD `RLIMIT_AS` precedent — the worked example

OpenBSD is the **only** target whose kernel ABI omits `RLIMIT_AS`
(`syscall.RLIMIT_AS` is absent). Referencing it unconditionally was a compile
error on OpenBSD — a build-bar failure. The fix is the model for every future ABI
gap: split the rlimit table by build tag rather than guard at runtime.

- `limittable_as.go` (`//go:build unix && !openbsd`) maps `ResourceAS → RLIMIT_AS`.
- `limittable_openbsd.go` (`//go:build openbsd`) is a no-op: it adds no extra
  resource, leaving `ResourceAS` unmapped.

On OpenBSD a `Spec` requesting `ResourceAS` now surfaces the typed
`UnknownResource` (`0.2.6.13`) — the honest *"this kernel cannot set that limit"*
answer — instead of failing to compile. **Missing kernel ABI constant → honest
typed error, not a build break.**

## Native-backend roadmap

Today every package clears the build bar and returns a uniform `UnsupportedPlatform`
(or `UnknownResource`) where it has no native mechanic. The gaps are resource
control (`cgroup` / `rlimit`) and process reaping (`reaper`) off Linux. The
adequate native mechanics, researched, are upgraded **incrementally** — each lands
behind the existing port and is validated on the real kernel by `e2e-vm`.

### Per-domain portability verdict

| Package | Linux | Darwin | FreeBSD | OpenBSD | NetBSD | DragonFly | Windows | Uniformity mechanic |
|---|---|---|---|---|---|---|---|---|
| `proc/exec` (spawn) | ✅ | ✅ | ✅ | ✅¹ | ✅ | ✅ | ⚠️ `UnsupportedPlatform` | `_unix`/`_other` split |
| `proc/signal` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ portable subset | name↔number table build-tagged |
| `proc/rlimit` | ✅ | ✅ | ✅ | ✅¹ | ✅ | ✅ | ⚠️ `UnsupportedPlatform` | **gap: Windows Job Objects** |
| `proc/reaper` (subreaper) | ✅ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ N/A | **gap: BSD `procctl`** |
| `proc/cgroup` | ✅ cgroup v2 | ❌→stub | ❌→stub | ❌→stub | ❌→stub | ❌→stub | ❌→stub | **gap: FreeBSD `rctl`, Windows Job Objects** |
| `proc/sdnotify` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ `UnsupportedPlatform` | sd_notify datagram is portable across unix |
| `proc/sdlisten` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ `UnsupportedPlatform` | fd inheritance is unix; `_unix`/`_other` split |

¹ OpenBSD: no `RLIMIT_AS` (→ `UnknownResource`); otherwise native.

### Resource control — `cgroup` / `rlimit`

| Platform | Mechanism | Surface |
|---|---|---|
| Linux | cgroup v2 (`memory.max`/`cpu.max`/`pids.max`/`cgroup.kill`) + `setrlimit`/`prlimit64` | implemented |
| **FreeBSD** | **`rctl(8)`** (RACCT/RCTL kernel): `memoryuse`, `pcpu`, `maxproc`, `vmemoryuse`, applied per-process/jail | `rctl -a process:<pid>:memoryuse:deny=<n>` |
| **Windows** | **Job Objects**: `CreateJobObject` + `SetInformationJobObject` with `JOBOBJECT_EXTENDED_LIMIT_INFORMATION` (memory/process) + `JOBOBJECT_CPU_RATE_CONTROL_INFORMATION` (cpu); assign the child to the job | raw `syscall` to `kernel32.dll` |
| OpenBSD / NetBSD / DragonFly | `setrlimit` + `login.conf` classes (no cgroup-equivalent grouping) | partial — rlimit only |

### Process reaping — `reaper`

| Platform | Mechanism |
|---|---|
| Linux | `prctl(PR_SET_CHILD_SUBREAPER)` + SIGCHLD `wait4` loop (implemented) |
| **FreeBSD / DragonFly** | **`procctl(PROC_REAP_ACQUIRE)`** — become a reaper for the descendant tree |
| OpenBSD / NetBSD | no subreaper; PID1-only reaping |
| Windows | N/A — no zombies; lifetime via Job Objects `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` |

### Dependency constraint on the native backends

`golang.org/x/sys` is **banned** SDK-wide (the dep-light invariant, ADR 0016).
Native backends therefore use **raw stdlib `syscall`** with the ABI constants and
struct layouts hand-defined and **cited** in the source (the same discipline as
the existing `proc` trampoline). The Windows Job Object structs and the FreeBSD
`rctl`/`procctl` constants are declared in-tree, never imported. Reference:
`github.com/aoldershaw/proclimit` unifies Linux cgroups + Windows Job Objects
behind one Go API — a model for the `Group` port's native backends, not a dependency.

## Consequences

- **Positive.** The build bar is enforced **forever**: no platform-specific call
  can silently drop a package, because the cross-compile matrix gates every PR.
- **Positive.** Consumers get **one uniform contract today** — the same typed
  `UnsupportedPlatform` / `UnknownResource` everywhere a native mechanic is
  missing — so downstream code branches on a typed error, never on a build tag.
- **Positive.** Native backends land **incrementally** behind the unchanged port;
  each is validated on the real kernel by `e2e-vm`, so a backend is never declared
  done on a green cross-compile alone.
- **Negative / accepted.** The runtime-bar workflow (`e2e-vm.yml`) is
  `workflow_dispatch`-only until the org's self-hosted runners are registered and
  the Proxmox secrets (`CI_RUNNER_API_TOKEN_ID/SECRET`, `PROXMOX_NODE`,
  `ADMIN_USERNAME`) are wired. A missing-infra org must not hard-fail every PR; it
  is promoted to `pull_request` once the labs runners are live.
- **Negative / accepted.** Hand-defined ABI constants for the native backends must
  be kept in sync with each kernel's headers by hand (the `x/sys` ban's cost). The
  citations in source are the mitigation; the e2e-vm runtime bar is the safety net.

## Why not …

- **Allow `golang.org/x/sys` for the off-Linux backends.** It would supply the
  Windows / BSD constants ready-made, but it inverts the SDK's dep-light invariant
  (ADR 0016 §Dependency discipline) — `pkg` consumers stay free of it. Rejected;
  hand-define + cite instead.
- **Runtime capability checks instead of build-tag splits.** A runtime guard on a
  missing constant still fails to *compile* where the constant is absent (the
  OpenBSD `RLIMIT_AS` case). The build-tag table split is the only thing that keeps
  the cross-compile green. Rejected as a general rule.
- **Skip the runtime bar; trust the cross-compile.** A green `go build` proves the
  symbols resolve, not that the syscall behaves. The `proc` primitives are exactly
  the class where behaviour diverges per kernel. Rejected — hence `e2e-vm`.

## References

- ADR 0016 (process-supervision domain), ADR 0004 (Bazel / Linux CI), ADR 0005 (`UnsupportedPlatform` sentinel)
- `scripts/cross-platform-audit.sh` — local build-bar matrix
- `.github/workflows/cross-platform.yml` — build-bar CI gate
- `.github/workflows/e2e-vm.yml` — runtime-bar real-kernel gate
- FreeBSD `rctl(8)` / RACCT/RCTL; `procctl(2)` `PROC_REAP_ACQUIRE`
- Windows Job Objects — `SetInformationJobObject`, `JOBOBJECT_EXTENDED_LIMIT_INFORMATION`, `JOBOBJECT_CPU_RATE_CONTROL_INFORMATION`
