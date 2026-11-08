<!-- updated: 2026-11-08T15:00:00Z -->
# pkg/v1/proc/service/

## Purpose

Public facade of the service engine (ADR 0214): `internal/service/proc/service`
runs one unit, `internal/core/proc/service` holds its contract. Everything here
is kit's (`gen_facade.go`, ADR 0166): aliases, re-exported constants and codes,
five forwarders. The README is generated from `docs/api` (`make
docs-readme`); the package comment is the design's (`design/proc/service.yaml`).

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Config`, `Unit` | alias | the engine's; `Unit.Subscribe` hands out a channel and its cancel |
| `Spawner`, `Attacher`, `CgroupKill`, `NotifySource`, `Clock` | alias, port | the core's; frozen (ADR 0039) |
| `CgroupMembers` | alias, port | `CgroupKill`'s sibling: a `Type=forking` unit's guess (S0c), the subtree's members |
| `BusWatcher` | alias, port | who owns a `Type=dbus` unit's `BusName=` (S0e), `Config.BusWatcher`; `pkg/v1/proc/dbus`'s `Conn` is one |
| `CgroupUnit` | alias, port | `CgroupKill`'s sibling the cgroup modes run on (S0g): `KillMode=control-group` (the zero with one) and `mixed`, `ExitType=cgroup`, `Delegate=`, `OOMPolicy=`; proc/cgroup's Linux group is one, its `Membership` turning the group's directory into an `AttachScope`'s cgroup |
| `Type`, `Restart`, `RestartMode`, `KillMode`, `ExitType`, `NotifyAccess`, `TimeoutMode`, `OOMPolicy`, `Preserve`, `Toggle`, `ProtectSystem`, `PrivateTmp`, `State`, `Sub`, `Result`, `CommandFlags` | alias | and every constant of each, re-exported — `SubRunningRevalidating`, a `Type=dbus` unit up without its name, among them (S0e) |
| `BindPath`, `TemporaryFileSystem`, `SystemCallFilter` | alias | `BindPathValue`, `TemporaryFileSystemValue`, `SystemCallFilterValue`: the sandbox directives' values (#728 wired into the engine) |
| `Command`, `ExitStatusSet`, `Notification`, `Snapshot`, `Event`, `StoredFD` | alias | `CommandValue`, `ExitStatusValue`, `NotifyValue`, `StateValue`, `EventValue`, `StoredFDValue` (S0d) |
| `Infinity`, `Zero` | const | systemd's `infinity` and literal `0` |
| `NewUnit`, `ParseCommand`, `ListenNotify`, `NewAttacher`, `SpawnInstance` | func | forwarders; `NewAttacher` is the `Attacher` over proc/exec (#729); `SpawnInstance` a unit per accepted connection (S0f) |
| `AttachScope` | alias | `coreproc.AttachValue`, the scope `NewAttacher` bounds an untrustworthy PID file by — the same type as proc/process's |
| `Code*` | const | `0.3.105.*`, for `errs.HasCode` |

## Do NOT

- Add a body here: a behaviour is the engine's, a value the core's.
- Import `pkg/v1/clock`: `Clock` is the kernel's type, which `pkg/v1/clock`
  aliases too (ADR 0214 §5), so proc requires nothing new.
