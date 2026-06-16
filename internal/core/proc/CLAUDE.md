# internal/core/proc/

## Purpose

Declares the **OS process-supervision contract** of the SDK: the ports
(`Process`, `Reaper`, `Group`, `Listener`), the immutable value types (`Spec`,
`ExitValue`, `LimitValue`, `NotificationValue`, `Signal`, `Resource`), and the
domain's **complete error-sentinel set** (range `0.2.6.*`). Sixth `internal/core`
sibling, admitted by ADR 0016, peer of `codec` / `writer` / `crypto` / `logger`
/ `transform`.

This is a **foundation** package: it carries the shared contract consumed by the
service implementations under `internal/service/proc/*` and re-exported by six
`pkg/v1` facades (`process`, `signal`, `reaper`, `rlimit`, `cgroup`, `sdnotify`).
It has **no plug-in registry** — unlike codec/crypto, each primitive has a single
canonical OS implementation chosen at build time by platform tag, not a
runtime-registered scheme.

Code range: `0.2.6.*` (ADR 0016).

## Contents

| File | Surface |
|---|---|
| `proc.go` | package doc + `Resource` enum (`NoFile`/`NProc`/`Core`/`AS`/`CPU`/`FSize`/`Data`/`Stack`/`MemLock`) + `String`/`Known` |
| `signal.go` (+ `signal_unix.go` / `signal_other.go`) | `Signal` value type: `Parse` / `String` / `OS` / `Int` / `Known`; platform name table |
| `spec.go` | `Spec` — process spawn spec (path/args/dir/env, creds, pgroup/session, rlimit/nice/umask/oom, **CgroupPath** for pre-exec cgroup v2 placement, **stdio**, **ExtraFiles** for socket activation) |
| `stdio.go` | `StdioMode` — how a child's stdin/stdout/stderr are wired (`StdioInherit`/`StdioNull`/`StdioCapture`) + `String`/`Known` |
| `exit.go` | `ExitValue` — exit code, terminating signal, CPU times, max RSS + `Success` |
| `limit.go` | `LimitValue` — soft/hard rlimit pair + `LimitInfinity` |
| `notification.go` | `NotificationValue` — parsed sd_notify datagram; `Ready`/`Reloading`/`Stopping`/`Watchdog` read `State` |
| `process.go` | `Process` interface — `PID` / `Wait` / `Signal` / `SignalGroup` / `Stop` |
| `reaper.go` | `Reaper` interface — `Start` / `Stop` / `ReapOnce` |
| `group.go` | `Group` interface — cgroup v2 `SetMemoryMax` / `SetCPUMax` / `SetPidsMax` / `SetIOMax` / `Add` / `Kill` / `Freeze` / `Thaw` / `Delete` |
| `listener.go` | `Listener` interface — sd_notify supervisor side `Recv` / `Close` |
| `codes.go` | `Code*` constants — range `0.2.6.*` |
| `errors.go` | the 22 domain sentinels (`UnsupportedPlatform` … `CredentialMismatch`) |

## Central error allocation

The **whole domain** owns the single `0.2.6.*` PP octet and declares every
sentinel here. Service implementations and the `pkg/v1` facades only
`errs.Wrap` these sentinels — they declare **zero** new codes. This keeps the
six facades developable in parallel with no shared-file contention and keeps the
AST audit's view (only `core/proc` calls `errs.Define`) trivially unique. If a
new failure mode appears, add its sentinel **here**, never in a leaf package.

## Conventions

- **Interface-first.** Ports are interfaces; value types are immutable structs.
  Every method with a non-trivial body lives in `internal/service/proc/*`. The
  only bodies here are pure value-type methods (`Signal.Parse/String`,
  `Resource.String`, `ExitValue.Success`, `NotificationValue.*`) — mirroring the
  `logger/level` precedent.
- **`Signal` is platform-portable.** Its underlying value is the platform signal
  number; the name↔number table is build-tagged (`signal_unix.go` /
  `signal_other.go`) so `String`/`Parse` track the host kernel. No `init()` — the
  table is a `var` literal (`KTN-FUNC-NOINIT`).
- **`Resource` is abstract.** It is a portable enum; the `RLIMIT_*` mapping is a
  service-layer concern, so core never imports a platform rlimit constant.
- **Imports allowed**: stdlib (`os`, `syscall`, `time`, `strconv`, `strings`) +
  `internal/kernel/errs`. No `golang.org/x/sys`, no `internal/service/*`, no
  `pkg/*`.

## Do NOT

- Add a plug-in registry — `proc` selects implementations by build tag, not by
  runtime registration.
- Define a new error code in a service or facade package — all sentinels are
  central here (`0.2.6.*`).
- Put platform syscalls or a concrete `Process`/`Reaper`/`Group`/`Listener`
  implementation here — those live in `internal/service/proc/*`.
- Grow the value set with policy (restart, backoff, health) — that is the
  consumer's concern (superviz.io), not the SDK's.

## Verification

```sh
bazel test --config=race //internal/core/proc:proc_test
# Fallback
cd internal/core && GOWORK=off go test -race -cover ./proc/...
```
