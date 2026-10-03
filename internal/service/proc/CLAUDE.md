<!-- updated: 2026-10-03T12:00:00Z -->
# internal/service/proc/

## Purpose

The proc family's engines (ADR 0155): the concrete halves of the
process-supervision contract `internal/core/proc` (ADR 0016), the two systemd
protocols a supervised service speaks, and the private socket processes of one
machine talk over. This directory holds no Go code: it is a prefix, not a
package, and nothing imports `internal/service/proc` itself. Each member is a
package of the `internal/service` module with its own `CLAUDE.md`, and each
facade sits under `pkg/v1/proc` — at the member's own path, except that `exec`
and `self` are both published by `process` and `childwait` by none.

## The rule that put them together

A domain belongs here when what it acts on is an operating-system PROCESS and
the kernel mechanics around one: a child it spawns, signals, reaps, limits,
confines or waits for (`exec`, `childwait`, `signal`, `reaper`, `rlimit`,
`cgroup`), the process it runs in (`memlimit`, `self`), the supervisor that
started it (`systemd/notify`, `systemd/listen`), and another process on the
same machine (`ipc`). Bytes between processes over a network socket are the net
family's (`internal/service/net`); `ipc` is here because its whole gate is the
machine's — a directory only the account can reach, and the kernel's word on
the peer.

The two systemd protocols are grouped under `systemd/`, a directory of its own
(`systemd/CLAUDE.md`): the packages were `sdnotify` and `sdlisten`, and are
`notify` and `listen` at the path that now says `systemd`.

## Members

| Package | What it is | Built on | Code range | Facade |
|---|---|---|---|---|
| `exec/` | the one spawn of the domain: a `Spec` turned into a running child under explicit credentials, in its own process group and session, stopped by a SIGTERM→SIGKILL escalation (ADR 0016) | `core/proc.Process` | none (core `0.2.6.*`) | `pkg/v1/proc/process` |
| `childwait/` | the ledger that hands a child's exit status to the `Process` that owns it, whichever of `exec` and `reaper` collected it (ADR 0093) | none — shared by `exec` and `reaper` | none | none |
| `signal/` | typed subscription to signals and their relay to a pid or a process group | `core/proc.Signal` | none (core `0.2.6.*`) | `pkg/v1/proc/signal` |
| `reaper/` | PID 1 / subreaper zombie collection through `childwait` — also a sweep once a second on illumos and Solaris (ADR 0093, ADR 0144) | `core/proc.Reaper` | none (core `0.2.6.*`) | `pkg/v1/proc/reaper` |
| `rlimit/` | per-process resource ceilings through `setrlimit(2)` / `prlimit64(2)` | `core/proc.Resource`, `LimitValue` | none (core `0.2.6.*`) | `pkg/v1/proc/rlimit` |
| `cgroup/` | control groups: cgroup v2 on Linux, a Job Object on Windows, `rctl(8)` on FreeBSD | `core/proc.Group` | none (core `0.2.6.*`) | `pkg/v1/proc/cgroup` |
| `memlimit/` | the runtime soft memory limit derived from the control-group allowance already bounding this process (ADR 0075) | `core/proc.MemoryLimitValue` | none | `pkg/v1/proc/memlimit` |
| `self/` | what the running process says about itself: the build it came from and its runtime state (ADR 0100) | none — values of its own | none | `pkg/v1/proc/process` (`Self`, `Build`, `ParseBuild`) |
| `systemd/notify/` | sd_notify(3): the notifier, bounded by its caller's context (ADR 0072), and the supervisor's listener with the kernel-verified sender on Linux | `core/proc.Listener`, `NotificationValue` | none (core `0.2.6.*`) | `pkg/v1/proc/systemd/notify` |
| `systemd/listen/` | socket activation, sd_listen_fds(3): the inherited listeners, and `Prepare` for the activator | `core/proc.Spec` (`ExtraFiles`) | none (core `0.2.6.*`) | `pkg/v1/proc/systemd/listen` |
| `ipc/` | a private socket between processes of one machine: the directory gates it, the path above it is audited with `kernel/fs/pathchain`, `SO_PEERCRED` names the peer on Linux, a named pipe with its own DACL on Windows (ADR 0148) | none — no core counterpart (ADR 0074) | `0.3.91.*` | `pkg/v1/proc/ipc` |

Only `ipc` declares a code. Every other member that reports a failure wraps a
`0.2.6.*` sentinel of `internal/core/proc`, the domain's one block (ADR 0016):
`childwait` hands its causes to `exec` and `reaper`, which wrap them, and
`memlimit` and `self` return values, not errors. `ipc`'s range kept its value
when the package moved here from the root of `internal/service` (ADR 0160):
`codeRangeOwners` names `internal/service/proc/ipc` under the same key
`0x00_03_5B_00`, and `//:audit_sources` lists it by its new label.

Two members import a sibling, and say so: `exec` and `reaper` share
`childwait`, because the kernel hands a zombie's status to exactly one wait.
`exec` and `rlimit` share a helper instead, `internal/rlim`, the constructor of
the kernel's `syscall.Rlimit` whose field width FreeBSD and DragonFly declare
differently: one function is not worth an edge from one engine to the other, so
it sits under `internal/`, where Go's rule confines it to this family
(`internal/CLAUDE.md`).
Outside the family, `internal/service/app/health` and `internal/service/app/lifecycle`
import `systemd/notify`, and `internal/service/net/server` imports
`systemd/listen` to adopt a socket a supervisor passed.

## Do NOT

- Put Go code in this directory. A file here would make `proc` a service
  package of its own, beside the contract that already carries the name.
- Declare an error code in a member other than `ipc`: add the sentinel to
  `internal/core/proc` and wrap it (`internal/core/proc/CLAUDE.md`).
- Reach `golang.org/x/sys` from a member: every one is the standard library
  plus the kernel, and `syscall.NewLazyDLL` where Windows needs a kernel32
  entry point.
- Import one member from another without saying so in both `CLAUDE.md` files.
