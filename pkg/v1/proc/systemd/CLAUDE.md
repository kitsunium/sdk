<!-- updated: 2026-10-03T04:55:00Z -->
# pkg/v1/proc/systemd/

## Purpose

The proc family's two systemd facades (ADR 0155). This directory holds no Go
code: it is a prefix, not a package, and there is no
`github.com/kitsunium/sdk/pkg/v1/proc/systemd` to import. Each member is a
package of the SDK module with its own `CLAUDE.md` and `README.md` (generated
by gomarkdoc from its doc comment, ADR 0008), imported by its full path —
`github.com/kitsunium/sdk/pkg/v1/proc/systemd/notify` — and linking what it
imports and never this directory or `pkg/v1/proc` above it.

## The rule that put them together

A facade belongs here when it publishes a protocol libsystemd defines between a
service and its service manager, implemented on the standard library so any
supervisor can speak it: readiness and the watchdog (`notify`, sd_notify(3))
and socket activation (`listen`, sd_listen_fds(3)). Their engines sit at the
same paths under `internal/service/proc/systemd`, over the one contract
`internal/core/proc`, and the capability preflight names them
`proc.CapSdNotify` and `proc.CapSocketActivation`.

## Members

| Package | What it publishes | Aliases onto | README |
|---|---|---|---|
| `notify/` | `Notify`, `Ready`, `Reloading`, `Stopping`, `Status`, `Watchdog`, `MainPID`, `WatchdogInterval`, and `Listen` → a `Listener` yielding `Notification`s | `internal/core/proc`, `internal/service/proc/systemd/notify` | `notify/README.md` |
| `listen/` | `Files`, `Listeners`, `WithNames` for the service, `Prepare` for the activator, over `Spec` | `internal/core/proc`, `internal/service/proc/systemd/listen` | `listen/README.md` |

Both moved here from the root of `pkg/v1`, where they were the packages
`sdnotify` and `sdlisten`, in one minor release, with no alias left at the old
paths (ADR 0155 §4, extending ADR 0040), and both were renamed for the path
they sit at: a caller writes `notify.Ready()` and `listen.Listeners(true)`.

## Do NOT

- Put Go code in this directory: it would publish a package nobody designed, at
  the path both protocols would then appear to belong to.
- Declare an error code in a member: the sentinels are `internal/core/proc`'s
  (`0.2.6.*`), and the members expose them through that contract.
- Hand-edit a member's `README.md`: edit its doc comment and run
  `make docs-readme`, whose `--repository.path` names the member's full path.
- Leave an alias package at either old path, at the root of `pkg/v1`.
