<!-- updated: 2026-11-08T00:00:00Z -->
# internal/core/proc/

## Purpose

Declares the **OS process-supervision contract** of the SDK: the ports
(`Process`, `Reaper`, `Group`, `Listener`, and `Process`'s sibling ports
`ContextWaiter`, `ProcessHandle` and `Terminal` — ADR 0039, ADR 0217), the
immutable value types (`Spec`, `ExitValue`, `UsageValue`, `LimitValue`,
`NotificationValue`, `DescriptorsValue`, `AttachValue`, `Signal`, `Resource`, `StdioMode`, `PTYValue`,
`SchedulingValue`, `SchedPolicy`, `IOClass`, `RunValue`, `ResultValue`,
`MemoryLimitValue`, `MemorySource`), and the
domain's **complete error-sentinel set** (range `0.2.6.*`). Sixth `internal/core`
sibling, admitted by ADR 0016, peer of `codec` / `writer` / `crypto` / `logger`
/ `transform`.

This is a **foundation** package: it carries the shared contract consumed by the
service implementations under `internal/service/proc/*` and re-exported by nine
`pkg/v1` facades — `proc`, the capability preflight at the family's root, and
its children `process`, `signal`, `reaper`, `rlimit`, `cgroup`,
`systemd/notify`, `systemd/listen` and `memlimit` under `pkg/v1/proc` (ADR 0155).
`ipc`, the family's private socket, does not build on this contract: its own
— the `Listener` and `Dialer` ports, `PeerValue`, `Conn` and the `0.3.91.*`
codes — is the child package `internal/core/proc/ipc`, at the path that
mirrors its engine (ADR 0160).
It has **no plug-in registry** — unlike codec/crypto, each primitive has a single
canonical OS implementation chosen at build time by platform tag, not a
runtime-registered scheme.

Code range: `0.2.6.*` (ADR 0016).

**The ports are generated from the design** (ADR 0163): `Process`, `Reaper`,
`Group` and `Listener` are declared, with their doc comments, under `ports:` in
`design/proc.yaml`, and `kit gen` writes them into `gen_ports.go`. A port or
its doc comment changes in the design, then `kit gen`, then `make api` — never
in `gen_ports.go`, whose header digests `make api-check` verifies. They moved
there from `process.go`, `reaper.go`, `group.go` and `listener.go`, content
moved and never deleted. The four files, left with nothing but their package
comment, are gone (rule 5);
the package comment is `doc.go`'s alone, written by kit from the design (ADR 0167).

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `proc.go` | `Resource` enum (`ResourceNoFile`/`ResourceNProc`/`ResourceCore`/`ResourceAS`/`ResourceCPU`/`ResourceFSize`/`ResourceData`/`ResourceStack`/`ResourceMemLock`; the zero is `ResourceUnknown`) + `String`/`Known` |
| `signal.go` (+ `signal_unix.go` / `signal_other.go`) | `Signal` value type: `Parse` / `String` / `OS` / `Int` / `Known`; platform name table |
| `signal_rt_linux.go` / `signal_rt_freebsd.go` / `signal_rt_other.go` | the realtime range (#942): `SIGRTMIN`..`SIGRTMAX` = 34..64 on Linux (glibc's view: the kernel's 32 and 33 are glibc's), 65..126 on FreeBSD, empty elsewhere; `Parse` reads `SIGRTMIN+n` / `SIGRTMAX-n` / the number, `String` writes `SIGRTMIN+n`, `Known` is true in range |
| `spec.go` | `Spec` — process spawn spec (path/args/dir/env, creds, pgroup/session, **DieWithParent**, rlimit/nice/umask/oom, **Scheduling**, **CgroupPath** for pre-exec cgroup v2 placement, **stdio** with **PTY** and **DrainGrace**, **ExtraFiles** for socket activation, **FDs** — ADR 0217, **PIDEnv** — names set to the child's own PID after the fork, LISTEN_PID's way, #739, **Sandbox** — #728); `FDPolicy` (`FDsSealed`/`FDsInherited`) and `PTYValue`, the types only `Spec` names. `Path` is a file path as written, or a bare name the service searches in the CHILD's PATH (`Env`'s, else the parent's) with os/exec's rules |
| `attach.go` | `AttachValue` — the bounds an untrustworthy PID file's process must meet: a cgroup v2 subtree, a set of users (#729) |
| `stdio.go` | `StdioMode` — how a child's stdin/stdout/stderr are wired (`StdioInherit`/`StdioNull`/`StdioCapture`/`StdioPTY`) + `String`/`Known` |
| `scheduling.go` | `SchedulingValue`, `SchedPolicy` (`SchedOther`…`SchedIdle`, Linux's `SCHED_*` numbers) and `IOClass` (`IOClassNone`…`IOClassIdle`) + `String` (#940) |
| `run.go` | `RunValue` and `ResultValue` — `exec.Run`'s ceiling and grace, and its exit with the head of each stream (#935) |
| `exit.go` | `ExitValue` — exit code, terminating signal, **CoreDumped**, CPU times, max RSS, **Usage** (by value, `Known` false where the platform gives none — #1136) + `Success` (pointer receiver since #1136: 112 bytes); `UsageValue` — `Known`, faults, block I/O, context switches (#1090) |
| `limit.go` | `LimitValue` — soft/hard rlimit pair + `LimitInfinity` |
| `memory.go` | `MemoryLimitValue` — one soft-memory-limit derivation (`Allowance` / `Limit` / `Source` / `Bound`) + `Applied` — `MemorySource` (`MemorySourceOperator` / `MemorySourceUnconstrained` / `MemorySourceBelowFloor` / `MemorySourceCgroup`, the zero left unminted) + `String` (ADR 0075), and `MemoryBound` (`MemoryBoundHigh` / `MemoryBoundMax`, the zero unminted) + `String` — which file set the allowance (#1094) |
| `groupstat.go` | `GroupStatValue` (CPU usage/user/system, throttling, memory current/peak, `MemoryEventsValue`, pids, `[]IOStatValue`, `Partial`) — what `GroupReader.Stat` reads (#932) |
| `groupctl.go` | `Controller` — a cgroup v2 controller (`ControllerCPU` … `ControllerMisc`, Linux 6.12's eight, the zero unminted) + `String` (the kernel's name) / `Known` — and `GroupEventsValue` (`MemoryEvents`, `Populated`, `Frozen`) — what `GroupWatcher.Watch` reads (#727) |
| `notification.go` | `NotificationValue` — parsed sd_notify datagram: `State`, `Status`, `MainPID`, the kernel-verified sender (`SenderPID`, `SenderUID`, `SenderGID`), the `Descriptors` it brought; `DescriptorsValue` — `Files`, `MainPIDFD`, `SenderPIDFD`, the caller's (#730; one pointer, so the value stays 64 bytes for its value receivers); `Ready`/`Reloading`/`Stopping`/`Watchdog` read `State` |
| `gen_ports.go` | GENERATED by `kit gen` from `design/proc.yaml` (ADR 0163) — `Process` — `PID` / `Wait` / `Signal` / `SignalGroup` / `Stop`; `Reaper` — `Start` / `Stop` / `ReapOnce`; an implementation hands the status of a child a `Process` spawned to that `Process` instead of discarding it (ADR 0093); `Group` — cgroup v2 `SetMemoryMax` / `SetCPUMax` / `SetPidsMax` / `SetIOMax` / `Add` / `Kill` / `Freeze` / `Thaw` / `Delete`; `GroupReader` — its read sibling `Stat` / `Members` (#932, ADR 0039); the Linux siblings of #727 — `GroupTuner` (thirteen typed v2 setters), `GroupTree` (`Path` / `EnableControllers` / `Delegate` / `DeleteTree`), `GroupWatcher` (`Watch`), `GroupSignaler` (`Signal` / `Contains`) — and Windows' `JobLimiter` (`SetJobMemoryLimit`); `Listener` — sd_notify supervisor side `Recv` / `Close` |
| `sandbox.go` | `SandboxValue` (`Spec.Sandbox`, #728, ADR 0217 §Amendment 2026-11-08) and its parts: `Namespace` (`Namespace*` bits), `IDMapValue`, `MountValue` with `MountKind` (`MountReadOnly` the zero value, `MountBind`, `MountTmpfs`, `MountTmpfsSticky` — mode 1777, `PrivateTmp=`), `CapabilitiesValue` (bounding, ambient, securebits masks), `LandlockValue` / `LandlockRuleValue` / `LandlockAccess` (`Landlock*` rights by ABI), `SeccompValue` / `SeccompAction` (`SeccompKill` the zero value, `SeccompErrno`, `SeccompAllow`). Values only: the service applies them |
| `gen_codes.go` | `Code*` constants — range `0.2.6.*`; the 36 domain sentinels (`UnsupportedPlatform` … `StdioCaptureFailed`, `StdioDrainTimeout`, `HandleUnavailable`, `PTYFailed` — ADR 0217; `MemlimitMisconfigured` — 0.2.6.28, #937; `CgroupReadFailed` — 0.2.6.29, #932; `ReservedEnv` — 0.2.6.31, #726; `PIDFileRefused`, `AdoptionRefused`, `StatusUnknown` — 0.2.6.32 to 0.2.6.34, #729; `InvalidFDName` — 0.2.6.35, #739; `CgroupControllerAbsent` — 0.2.6.37, #727; `SandboxFailed` — 0.2.6.30 and `SandboxUnavailable` — 0.2.6.36, #728) — written by kit gen from `design/proc.yaml` (ADR 0164) |

## Central error allocation

The **whole domain** owns the single `0.2.6.*` PP octet and declares every
sentinel here. Service implementations and the `pkg/v1` facades only
`errs.Wrap` these sentinels — they declare **zero** new codes. This keeps the
facades developable in parallel with no shared-file contention and keeps the
AST audit's view (only `core/proc` calls `errs.Define`) trivially unique. If a
new failure mode appears, add its sentinel **here**, never in a leaf package.

## Conventions

- **Interface-first.** Ports are interfaces; value types are immutable structs.
  Every method with a non-trivial body lives in `internal/service/proc/*`. The
  only bodies here are `Parse` and pure value-type methods
  (`Signal.String`/`OS`/`Int`/`Known`, `Resource.String`/`Known`,
  `StdioMode.String`/`Known`, `SchedPolicy.String`, `IOClass.String`,
  `Controller.String`/`Known`,
  `ExitValue.Success`, `NotificationValue.*`,
  `MemorySource.String`, `MemoryLimitValue.Applied`) — mirroring the
  `logger/level` precedent.
- **`Signal` is platform-portable.** Its underlying value is the platform signal
  number; the name↔number table is build-tagged (`signal_unix.go` /
  `signal_other.go`) so `String`/`Parse` track the host kernel. No `init()` — the
  table is a `var` literal (`KTN-FUNC-NOINIT`).
- **`Resource` is abstract.** It is a portable enum; the `RLIMIT_*` mapping is a
  service-layer concern, so core never imports a platform rlimit constant.
- **Imports allowed**: stdlib (`context`, `io`, `os`, `syscall`, `time`, `strconv`, `strings`) +
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
