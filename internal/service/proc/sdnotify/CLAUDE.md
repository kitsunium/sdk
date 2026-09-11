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
| `NotifyContext(ctx, state)` / `ReadyContext` / `StatusContext` | notifier, bounded | all (no-op when unset) |
| `WatchdogInterval()` | env query | all |
| `Listen() (Listener, path, err)` | supervisor | linux only |

## Protocol semantics (libsystemd-faithful)

- **`$NOTIFY_SOCKET` unset ⇒ no-op.** Every notifier function returns `nil` and
  sends nothing when `NOTIFY_SOCKET` is unset or empty. This is the documented
  sd_notify behaviour so an unsupervised binary stays silent rather than erroring.
- **Set-but-unreachable ⇒ `NotifyFailed`.** A dial/write failure against a
  configured socket is a real error (`coreproc.CodeNotifyFailed`).
- **The write is the only unbounded step, and the context siblings bound it**
  (ADR 0072). A unixgram write blocks once the RECEIVER's queue is full, and
  the receiver is the supervisor: measured on Linux, a brand new sender — which
  is what every send here is — parks at the 514th small datagram. `Notify` has
  no deadline and says so; `NotifyContext` hands ctx's deadline to the kernel as
  the socket's write deadline and reaches a write already parked through
  `context.AfterFunc`, which is the only way to interrupt a `net.Conn`. It
  bounds the WRITE and not the dial, since a unixgram dial takes no round trip.
  Only the two helpers with a caller that has a deadline to inherit have a
  sibling; the others gain one when something needs it.

  Its tests are **Linux-only** (`notify_bounded_linux_external_test.go`), and
  that is a property of the HARNESS, not of the bound: making a send block means
  filling the supervisor's receive queue, which is Linux's mechanism. CI showed
  the alternatives on the first run — FreeBSD accepted all 8192 fillers without
  ever blocking a sender, and macOS refused the send outright instead of parking
  it, so no deadline had anything to interrupt. Weakening the assertions to
  something all three satisfy would have kept them green while testing nothing.
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

## Performance — see `BENCH.md`

A `Ready()` / `Watchdog()` call is **0.7 % formatting and 99.3 % socket**
(168.8 ns to build `READY=1\n` and validate it, inside a 23.7 µs
dial/write/close). An UNSUPERVISED binary — `$NOTIFY_SOCKET` unset — pays
**83.7 ns and zero allocations** per call, so the shorthands can be shipped
unconditionally. There is no watchdog interval a supervisor would plausibly
configure at which this package becomes a cost.

Two consequences are pinned in the code:

- The field-injection check uses `strings.Contains(name, "\n") ||
  strings.Contains(name, "=")` rather than `strings.ContainsAny(name, "\n=")`.
  Both delimiters are ASCII and a UTF-8 continuation byte is never `0x0A` or
  `0x3D`, so the results are identical for every input; `ContainsAny` decodes a
  rune per byte for a name of ≤8 bytes (every sd_notify field name), which a
  profile named at 34 % of `encodePayload`. Worth **−24 %** on the function and
  −0.2 % on the call — kept because it is free, not because it matters.
- The `strings.Builder` is deliberately **not** pre-sized. Computing the exact
  size needs a second `range` over the map, and a map range costs ~60 ns of
  iterator setup — measured at **+29 % on `Ready()`**, which is a one-entry map
  like every other shorthand. The comment that used to claim pre-sizing now
  states the measurement instead.

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
