<!-- updated: 2026-11-08T15:00:00Z -->
# internal/core/proc/service/

## Purpose

The contract of a service unit's life cycle (ADR 0214): systemd.service(5)'s
directives as typed values, the states a unit passes through and the snapshot
a consumer reads, the four ports the engine reaches the world through and the
clock it waits on, `ParseCommand`, and the codes `0.3.105.*`. The engine is
`internal/service/proc/service`; the facade `pkg/v1/proc/service`. The second
proc domain with a core of its own, laid out as `ipc` is (ADR 0160).

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from `design/proc/service.yaml` |
| `values.go` | `Infinity`, `Zero` (systemd's `infinity` and literal `0`); the enumerations `Type`, `Restart`, `RestartMode`, `KillMode`, `ExitType`, `NotifyAccess`, `TimeoutMode`, `OOMPolicy`, `Preserve`, `Toggle`, `State`, `Sub`, `Result` |
| `names.go` | their spellings and methods: `String` in systemd's spelling for each, `Toggle.On`, `State.ActiveState` |
| `sandbox.go` | the sandbox directives' values (#728 wired into the engine, ADR 0214's amendment of 2026-11-08): `ProtectSystem` and `PrivateTmp` with their `String`, `BindPathValue`, `TemporaryFileSystemValue`, `SystemCallFilterValue` |
| `clock.go` | `Clock`, an alias of the kernel's `clock.Timed` (hand-written: kit writes no alias) |
| `command.go` | `CommandFlags` (`FlagArgv0` `@`, `FlagIgnoreFailure` `-`, `FlagNoEnvExpand` `:`, `FlagFullPrivileges` `+`, `FlagNoCredentials` `!`, `FlagShell` `\|`; `String`, `Privileged`), `CommandValue`, `ParseCommand` |
| `status.go` | `ExitStatusValue` (`Contains`, `Empty`), `NotifyValue`, `StateValue` (its `FDStore*` counters, S0d), `StoredFDValue` (one descriptor of a store, S0d), `EventValue` |
| `gen_ports.go` | GENERATED — `Spawner`, `Attacher`, `CgroupKill`, `NotifySource`, `BusWatcher` (S0e: `WatchName`, a `Type=dbus` unit's `BusName=` followed; `proc/dbus`'s `Conn` is one), and `CgroupKill`'s siblings: `CgroupMembers` (S0c; the subtree's members) and `CgroupUnit` (S0g: `Path`, `Members`, `Stat`, `Watch`, `Delegate`, `DeleteBeneath`, `SetMemoryOOMGroup` — what the cgroup modes need, the Linux group of `proc/cgroup` as it is) |
| `gen_codes.go` | GENERATED — `0.3.105.1`–`11` and their sentinels |

## Conventions

- **A zero is the directive's default** (ADR 0031): `TypeDefault` is dbus
  with a `BusName=`, else simple or oneshot by `ExecStart=`, `KillModeDefault` control-group with a `CgroupUnit`
  and process without (ADR 0214's S0g amendment), `OOMPolicyDefault` stop or
  continue under `Delegate=`, `Toggle` exists for the two booleans whose
  default is yes.
- **Spellings are systemd's**: `String` of every enumeration is the unit-file
  spelling, so the agent maps its YAML without a table of its own.
- **The ports are frozen** (ADR 0039): a later capability is a sibling
  interface the engine detects by assertion (S0g's `CgroupUnit`), or a new
  field of the engine's `Config` (S0e's `BusWatcher`).
- **`Sub` grows at its end**: `SubRunningRevalidating` (S0e, systemd 262's
  `running-revalidating`) is the last constant, so no value moved.
- **Bodies here** are `ParseCommand` and pure value methods only.

## Do NOT

- Put a spawn, a signal or a wait here — the engine's.
- Add a YAML key, a unit-file parser or a `%` specifier: the agent's.
- Declare a code outside `design/proc/service.yaml`.

## Verification

```sh
cd internal && go test -race ./core/proc/service/
```
