<!-- updated: 2026-10-03T04:55:00Z -->
# internal/service/proc/systemd/

## Purpose

The proc family's two systemd protocols (ADR 0155): what a service tells the
supervisor that started it, and the sockets that supervisor hands it. This
directory holds no Go code: it is a prefix, not a package, and nothing imports
`internal/service/proc/systemd` itself. Each member is a package of the
`internal/service` module with its own `CLAUDE.md`, published by the facade at
the same path under `pkg/v1/proc/systemd`.

## The rule that put them here

A package belongs here when it implements a protocol libsystemd defines between
a service and its service manager — and implements it without libsystemd, on
the standard library, so a supervisor that is not systemd speaks it too. Both
are documented in their `sd_*(3)` manual pages, and both carry their activator
half, so each is testable end to end without systemd.

The packages were `sdnotify` and `sdlisten`, named for their manual pages,
while they sat directly under `proc/`. Under `systemd/` the prefix said twice
what the path already says, so they are `notify` and `listen`: the package name
and the path's last element agree. Their files keep the protocol's name
(`sdnotify.go`, `sdlisten_unix.go`), and so does the `.ktn-linter.yaml` glob on
`notify/sdnotify.go`.

## Members

| Package | Protocol | What it is | In `pkg/v1` |
|---|---|---|---|
| `notify/` | sd_notify(3) | the notifier — `Notify` and the `Ready` / `Reloading` / `Stopping` / `Status` / `Watchdog` / `MainPID` shorthands, a no-op when `$NOTIFY_SOCKET` is unset, and the `*Context` siblings that bound the write (ADR 0072) — plus `Listen`, the supervisor's side, with the kernel-verified sender on Linux | `pkg/v1/proc/systemd/notify` |
| `listen/` | sd_listen_fds(3) | socket activation — `Files` / `Listeners` / `WithNames` recover the inherited sockets from fd 3, `LISTEN_PID` honoured, and `Prepare` hands sockets to a child `Spec` | `pkg/v1/proc/systemd/listen` |

Neither declares a code: both wrap the `0.2.6.*` sentinels of
`internal/core/proc` (`NotifyFailed`, `ListenFailed`, `InvalidNotification`,
`CredentialMismatch`, `UnsupportedPlatform`). Neither imports the other.

## Do NOT

- Put Go code in this directory.
- Link libsystemd or reach `golang.org/x/sys`: the protocols are a datagram and
  inherited descriptors, both in the standard library.
- Rename a member back to its `sd` prefix, or alias it so in a new importer:
  `internal/service/net/server` imports `listen` as `sdlisten` only because it
  declares its own `listen`.
