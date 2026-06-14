# sdnotify (service) — sd_notify(3) readiness/watchdog protocol

Concrete implementation of the systemd **sd_notify** protocol behind the
`internal/core/proc` `Listener` port and `NotificationValue` value type. Consumed
by the public `pkg/v1/sdnotify` facade — application code imports that, never this
package.

## Two sides

- **Notifier (child).** `Notify` and the shorthands `Ready` / `Reloading` /
  `Stopping` / `Status` / `Watchdog` / `MainPID` write a newline-separated
  `NAME=value` datagram to `$NOTIFY_SOCKET`. When `$NOTIFY_SOCKET` is unset every
  call is a no-op returning `nil` (libsystemd semantics). Portable across all
  GOOS.
- **Supervisor (listener).** `Listen` creates a private `unixgram` socket with
  `SO_PASSCRED` enabled and returns it plus its path; a child is pointed at it via
  `NOTIFY_SOCKET=<path>`. `Recv` yields one parsed, credential-verified
  `NotificationValue` per datagram, with `SenderPID` taken from the kernel's
  `SCM_CREDENTIALS`. Linux-only; `Listen` returns `UNSUPPORTED_PLATFORM`
  elsewhere.

`WatchdogInterval()` parses `$WATCHDOG_USEC` (microseconds) into a
`time.Duration`, reporting `ok=false` when unset or malformed.

## Platform

| File | Build tag | Role |
|---|---|---|
| `sdnotify.go` | — | parsing/encoding helpers, `WatchdogInterval` |
| `notify.go` | — | notifier (portable, no-op when unset) |
| `listen_linux.go` | `linux` | SO_PASSCRED credential listener |
| `listen_other.go` | `!linux` | `Listen` ⇒ `UnsupportedPlatform` |

## See also

- `internal/service/proc/sdnotify/CLAUDE.md` — maintainer notes
- `internal/core/proc/` — the port + value type + error sentinels
- ADR 0016 — `docs/adr/0016-sdk-process-supervision-domain.md`
