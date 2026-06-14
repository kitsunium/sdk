# internal/service/proc/sdnotify/

## Purpose

Concrete implementation of the **sd_notify(3)** readiness/watchdog protocol — both
the notifier (child) and listener (supervisor) sides — over the `Listener` port
and `NotificationValue` value type declared in `internal/core/proc`. Backs the
`pkg/v1/sdnotify` facade.

## Surface

| Symbol | Side | Platform |
|---|---|---|
| `Notify(state)` / `Ready` / `Reloading` / `Stopping` / `Status` / `Watchdog` / `MainPID` | notifier | all (no-op when unset) |
| `WatchdogInterval()` | env query | all |
| `Listen() (Listener, path, err)` | supervisor | linux only |

## Protocol semantics (libsystemd-faithful)

- **`$NOTIFY_SOCKET` unset ⇒ no-op.** Every notifier function returns `nil` and
  sends nothing when `NOTIFY_SOCKET` is unset or empty. This is the documented
  sd_notify behaviour so an unsupervised binary stays silent rather than erroring.
- **Set-but-unreachable ⇒ `NotifyFailed`.** A dial/write failure against a
  configured socket is a real error (`coreproc.CodeNotifyFailed`).
- **Abstract namespace.** A `$NOTIFY_SOCKET` value beginning with `@` selects the
  Linux abstract namespace; the `@` is replaced by a NUL byte in `sun_path[0]`
  before binding/connecting (`resolveAddr`).
- **Datagram body.** Newline-separated `NAME=value` fields (`READY=1`,
  `RELOADING=1`, `STOPPING=1`, `STATUS=…`, `MAINPID=…`, `WATCHDOG=1`). `STATUS`
  and `MAINPID` are lifted into the typed `NotificationValue` fields; a non-`=`
  line or a non-integer `MAINPID` yields `InvalidNotification`.
- **`WATCHDOG_USEC`.** Parsed as a base-10 microsecond count into a
  `time.Duration`; unset/garbage/zero ⇒ `ok=false`, mirroring
  `sd_watchdog_enabled`.

## Credential verification (SO_PASSCRED)

`Listen` binds a `unixgram` socket under a private `0700` temp dir and sets
`SO_PASSCRED` via `conn.SyscallConn()` + `syscall.SetsockoptInt`. `Recv` reads
with `ReadMsgUnix`, parses the OOB control data with
`syscall.ParseSocketControlMessage` + `syscall.ParseUnixCredentials`, and fills
`NotificationValue.SenderPID` with the **kernel-verified** sender PID — a value
the sender cannot forge. The temp dir (and socket inode) is removed on `Close`.

## Layering / platform split

- Imports stdlib (`net`, `os`, `syscall`, `strings`, `strconv`, `time`,
  `errors`, `path/filepath`) + `internal/core/proc` + `internal/kernel/errs`.
  Never imports `pkg/*` or `golang.org/x/sys`.
- `notify.go` + `sdnotify.go` are portable (no build tag).
- `listen_linux.go` (`//go:build linux`) holds the credential-passing listener;
  `listen_other.go` (`//go:build !linux`) returns `coreproc.UnsupportedPlatform`
  from `Listen`. The package compiles on every GOOS.

## Errors

No new codes are defined here. The package wraps the central `core/proc`
sentinels: `NotifyFailed`, `ListenFailed`, `InvalidNotification`,
`UnsupportedPlatform` (and is wired to restate `CredentialMismatch` if a future
caller layers PID-pinning on top of `SenderPID`). Match with
`errs.HasCode(err, coreproc.CodeX)`.

## Verification

```sh
go test -race ./...
ktn-linter lint ./...
```

The credential round-trip works in an unprivileged container (same-process
`SO_PASSCRED` send); the external tests degrade to `t.Skip` if `Listen` cannot
bind on a constrained host, while still asserting the typed-error contract.
